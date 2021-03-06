package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"

	"github.com/ardielle/ardielle-go/rdl"
	"github.com/ardielle/ardielle-tools/rdl-plugins/swagger"
)

//
// This command should take a filename as input, and spit out the JSON representation of an RDL schema as output.
//
func main() {
	if len(os.Args) != 2 {
		fmt.Println("usage: rdl-import-swagger swaggerfile.json")
		os.Exit(1)
	}
	name := os.Args[1]
	tmp := strings.Split(name, "/")
	name = tmp[len(tmp)-1]
	i := strings.LastIndex(name, ".")
	if i > 0 {
		name = name[:i]
	}
	data, err := ioutil.ReadFile(os.Args[1])
	if err != nil {
		fmt.Println("***", err.Error())
		os.Exit(1)
	}
	var doc *swagger.Doc
	err = json.Unmarshal(data, &doc)
	if err != nil {
		fmt.Println("***", err.Error())
		os.Exit(1)
	}
	schema, err := swaggerToSchema(name, doc)
	if err != nil {
		fmt.Println("***", err)
	}
	if schema != nil {
		fmt.Println(pretty(schema))
	}
}

func swaggerToSchema(name string, doc *swagger.Doc) (*rdl.Schema, error) {
	s := doc.Info.Title
	if strings.HasPrefix(s, "The ") && strings.HasSuffix(s, " API") {
		name = s[4 : len(s)-4]
	}
	sb := rdl.NewSchemaBuilder(name).Comment(doc.Info.Description)
	if doc.Info.Version != "" {
		n, err := strconv.Atoi(doc.Info.Version)
		if err == nil {
			sb.Version(int32(n))
		}
	}
	if doc.BasePath != "" {
		sb.Base(doc.BasePath)
	}
	for k, v := range doc.Definitions {
		importSwaggerType(sb, k, v, false)
	}
	for k, v := range doc.Paths {
		importSwaggerResources(sb, k, v)
	}
	return sb.BuildParanoid()
}

func importSwaggerResources(sb *rdl.SchemaBuilder, path string, handler *swagger.PathItem) {
	if handler.Get != nil {
		importSwaggerResource(sb, path, "get", handler.Get)
	}
	if handler.Put != nil {
		importSwaggerResource(sb, path, "put", handler.Put)
	}
	if handler.Post != nil {
		importSwaggerResource(sb, path, "post", handler.Post)
	}
	if handler.Delete != nil {
		importSwaggerResource(sb, path, "get", handler.Delete)
	}
	if handler.Options != nil {
		importSwaggerResource(sb, path, "options", handler.Options)
	}
	if handler.Head != nil {
		importSwaggerResource(sb, path, "head", handler.Head)
	}
	if handler.Patch != nil {
		importSwaggerResource(sb, path, "patch", handler.Patch)
	}
}

func importTypeName(tdef swagger.Type, simpleType string) string {
	if tdef["$ref"] != nil {
		ref := tdef["$ref"].(string)
		if strings.HasPrefix(ref, "#/definitions/") {
			return camelize(ref[14:])
		}
	}
	if tdef["type"] != nil {
		return canonicalTypeName(tdef["type"].(string))
	}
	return canonicalTypeName(camelize(simpleType))
}

func importSwaggerResource(sb *rdl.SchemaBuilder, path string, method string, op *swagger.Operation) {
	tname := "?"
	expected := "OK"
	alts := make([]map[string]string, 0)
	for scode, resp := range op.Responses {
		if scode == "default" {
			tname = importTypeName(resp.Schema, "?")
		} else {
			talt := importTypeName(resp.Schema, "?")
			alts = append(alts, map[string]string{"type": talt, "code": scode})
		}
	}
	var exceptions map[string]*rdl.ExceptionDef
	var alternatives []string
	for _, a := range alts {
		if tname == "?" {
			tname = canonicalTypeName(a["type"])
		} else if a["type"] == tname {
			alternatives = append(alternatives, a["code"])
		} else {
			if exceptions == nil {
				exceptions = make(map[string]*rdl.ExceptionDef)
			}
			exceptions[a["code"]] = &rdl.ExceptionDef{Type: a["type"]}
		}
	}
	rb := rdl.NewResourceBuilder(tname, strings.ToUpper(method), path).Comment(op.Summary)
	rb.Expected(expected)
	if len(alternatives) > 0 {
		//fmt.Println("FIXME: rdl.ResourceBuilder needs a .Alternative(code) method")
		//see below for just setting it after we build
	}
	if len(exceptions) > 0 {
		for k, v := range exceptions {
			rb.Exception(k, v.Type, v.Comment)
		}
	}
	if op.OperationID != "" {
		//only set this if it is not the default
		rezName := strings.ToLower(method) + tname
		if rezName != op.OperationID {
			rb.Name(op.OperationID)
		}
	}
	for _, prod := range op.Produces {
		if prod != "application/json" {
			fmt.Println("WARNING: expected to produce something other than application/json:", prod)
		}
	}
	for _, param := range op.Parameters {
		pparam := false
		qparam := ""
		header := ""
		switch param.In {
		case "path":
			pparam = true
		case "query":
			qparam = param.Name
		case "body":
		case "header":
			header = param.Name //this is an HTTP Header (a fairly general string), not an Identifier
		default:
			//not supported: formHeader
		}
		identifier := strings.Replace(param.Name, "-", "_", -1)
		optional := false
		var defval interface{}
		ptype := importTypeName(param.Schema, param.Type)
		rb.Input(identifier, ptype, pparam, qparam, header, optional, defval, param.Description)
	}
	r := rb.Build()
	if len(alternatives) > 0 {
		r.Alternatives = alternatives
	}
	if op.Tags != nil && len(op.Tags) > 0 {
		if r.Annotations == nil {
			r.Annotations = make(map[rdl.ExtendedAnnotation]string)
		}
		r.Annotations["x_tags"] = strings.Join(op.Tags, ",")
	}
	setDefaultParamTypes(r)
	sb.AddResource(r)
}

func setDefaultParamTypes(r *rdl.Resource) {
	//parse the path template
	path := r.Path
	i := strings.Index(path, "{")
	for i >= 0 {
		j := strings.Index(path[i:], "}")
		if j < 0 {
			fmt.Println("bad path template syntax: " + path)
			return
		}
		j += i
		name := path[i+1 : j]
		k := strings.Index(name, ":")
		if k >= 0 {
			if k == 0 {
				fmt.Println("Bad path template syntax: " + path)
			}
			name = name[0:k]
		}
		ok := false
		for _, in := range r.Inputs {
			if string(in.Name) == name {
				ok = true
				break
			}
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "*** Error: Resource input '%s' in '%s %s' has no corresponding type declaration.\n", name, r.Method, r.Path)
			os.Exit(1)
		}
		i = strings.Index(path[j+1:], "{")
		if i >= 0 {
			i += j + 1
		}
	}
}

func getString(m map[string]interface{}, k string) string {
	if o, ok := m[k]; ok {
		if s, ok := o.(string); ok {
			return s
		}
	}
	return ""
}

func getInt(m map[string]interface{}, k string) int32 {
	if o, ok := m[k]; ok {
		switch n := o.(type) {
		case int:
			return int32(n)
		case int32:
			return n
		case int64:
			return int32(n)
		case float32:
			return int32(n)
		case float64:
			return int32(n)
		}
	}
	return -1
}

func importSwaggerType(sb *rdl.SchemaBuilder, name string, def swagger.Type, fromFieldSpec bool) {
	if name == "ResourceError" {
		return
	}
	name = camelize(name)
	requiredFields := make(map[string]bool)
	if def["required"] != nil {
		required := def["required"].([]interface{})
		for _, r := range required {
			requiredFields[r.(string)] = true
		}
	}
	dtype := getString(def, "type")
	if dtype == "" {
		if def["properties"] != nil {
			dtype = "object"
		} else if def["items"] != nil {
			dtype = "array"
		}
	}
	switch def["type"] {
	case "object":
		tb := rdl.NewStructTypeBuilder("Struct", name).Comment(getString(def, "description"))
		if !fromFieldSpec {
			tb.Comment(getString(def, "description"))
		}
		if def["properties"] != nil {
			for fname, ofdef := range def["properties"].(map[string]interface{}) {
				fdef := ofdef.(map[string]interface{})
				optional := true
				if required, ok := requiredFields[fname]; required && ok {
					optional = false
				}
				ftype, _ := normalizeTypeName(fdef)
				if requiresTypeDef(fdef) {
					ftype = name + "_" + capitalize(fname)
					importSwaggerType(sb, ftype, fdef, true)
				} else {
					switch strings.ToLower(ftype) {
					case "bool", "string", "int32", "int16", "int8", "int64", "float64", "float32", "bytes":
					case "timestamp", "symbol", "uuid", "array", "map", "struct", "enum", "union":
					default:
						//user-defined type. Must resolve, no forward refs.
						//fmt.Println("typedef not required for field:", fname, "in type", name, "->", strings.ToLower(ftype))
					}
				}
				tb.Field(fname, ftype, optional, fdef["default"], getString(fdef, "description"))
			}
		}
		t := tb.Build()
		if def["example"] != nil && !fromFieldSpec {
			t.StructTypeDef.Annotations = addAnnotation(t.StructTypeDef.Annotations, "x_example", def["example"])
		}
		if def["properties"] != nil {
			for fname, ofdef := range def["properties"].(map[string]interface{}) {
				fdef := ofdef.(map[string]interface{})
				if fdef["example"] != nil {
					for _, f := range t.StructTypeDef.Fields {
						if f.Name == rdl.Identifier(fname) {
							f.Annotations = addAnnotation(f.Annotations, "x_example", fdef["example"])
						}
					}
				}
			}
		} else {
			t.StructTypeDef.Fields = make([]*rdl.StructFieldDef, 0)
		}
		sb.AddType(t)
	case "array":
		tb := rdl.NewArrayTypeBuilder("Array", name)
		if !fromFieldSpec {
			tb.Comment(getString(def, "description"))
		}
		if def["items"] != nil {
			ftype, _ := normalizeTypeName(def["items"].(map[string]interface{}))
			tb.Items(ftype)
		}
		t := tb.Build()
		if def["minItems"] != nil {
			t.ArrayTypeDef.Annotations = addAnnotation(t.ArrayTypeDef.Annotations, "x_minItems", def["minItems"])
		}
		if def["example"] != nil {
			t.ArrayTypeDef.Annotations = addAnnotation(t.ArrayTypeDef.Annotations, "x_example", def["example"])
		}
		if def["x-constraint"] != nil {
			for k, v := range def["x-constraint"].(map[string]interface{}) {
				//if k == "length" ...
				cname := "x_constraint_" + k
				if t.ArrayTypeDef != nil {
					t.ArrayTypeDef.Annotations = addAnnotation(t.ArrayTypeDef.Annotations, cname, v)
				} else if t.AliasTypeDef != nil {
					t.AliasTypeDef.Annotations = addAnnotation(t.AliasTypeDef.Annotations, cname, v)
				}
			}
		}
		sb.AddType(t)
	case "string":
		if def["enum"] != nil {
			tb := rdl.NewEnumTypeBuilder("Enum", name)
			if !fromFieldSpec {
				tb.Comment(getString(def, "description"))
			}
			elements := def["enum"].([]interface{})
			for _, e := range elements {
				tb.Element(e.(string), "")
			}
			sb.AddType(tb.Build())
			return
		}
		tb := rdl.NewStringTypeBuilder(name)
		if !fromFieldSpec {
			tb.Comment(getString(def, "description"))
		}
		pat := getString(def, "pattern")
		if pat != "" {
			tb.Pattern(pat)
		}
		maxlen := getInt(def, "maxLength")
		if maxlen >= 0 {
			tb.MaxSize(maxlen)
		}
		t := tb.Build()
		if def["example"] != nil && !fromFieldSpec {
			if t.StringTypeDef != nil {
				t.StringTypeDef.Annotations = addAnnotation(t.StringTypeDef.Annotations, "x_example", def["example"])
			} else if t.AliasTypeDef != nil {
				t.AliasTypeDef.Annotations = addAnnotation(t.AliasTypeDef.Annotations, "x_example", def["example"])
			}
		}
		if def["x-format"] != nil {
			for k, v := range def["x-format"].(map[string]interface{}) {
				aname := "x_format_" + k
				if t.StringTypeDef != nil {
					t.StringTypeDef.Annotations = addAnnotation(t.StringTypeDef.Annotations, aname, v)
				} else if t.AliasTypeDef != nil {
					t.AliasTypeDef.Annotations = addAnnotation(t.AliasTypeDef.Annotations, aname, v)
				}
			}
		}
		if def["x-constraint"] != nil {
			for k, v := range def["x-constraint"].(map[string]interface{}) {
				//if k == "length" ...
				cname := "x_constraint_" + k
				if t.StringTypeDef != nil {
					t.StringTypeDef.Annotations = addAnnotation(t.StringTypeDef.Annotations, cname, v)
				} else if t.AliasTypeDef != nil {
					t.AliasTypeDef.Annotations = addAnnotation(t.AliasTypeDef.Annotations, cname, v)
				}
			}
		}
		sb.AddType(t)
	case "integer":
		tb := rdl.NewNumberTypeBuilder("Int32", name)
		if !fromFieldSpec {
			tb.Comment(getString(def, "description"))
		}
		if def["x-constraint"] != nil {
			for k, v := range def["x-constraint"].(map[string]interface{}) {
				if k == "positive" {
					if v == true {
						tb.Min(0)
					}
				}
			}
		}
		t := tb.Build()
		if def["example"] != nil && !fromFieldSpec {
			t.NumberTypeDef.Annotations = addAnnotation(t.NumberTypeDef.Annotations, "x_example", def["example"])
		}
		sb.AddType(t)
	case "number":
		tb := rdl.NewNumberTypeBuilder("Float64", name)
		if !fromFieldSpec {
			tb.Comment(getString(def, "description"))
		}
		if def["x-constraint"] != nil {
			for k, v := range def["x-constraint"].(map[string]interface{}) {
				if k == "positive" {
					if v == true {
						tb.Min(0.0)
					}
				} else {
					fmt.Println("----- Unknown constraint:", k, v)
				}
			}
		}
		t := tb.Build()
		if def["example"] != nil && !fromFieldSpec {
			t.NumberTypeDef.Annotations = addAnnotation(t.NumberTypeDef.Annotations, "x_example", def["example"])
		}
		sb.AddType(t)
	default:
		fmt.Println("Unsupported top level type:", def)
	}
}

func requiresTypeDef(fdef swagger.Type) bool {
	if fdef["pattern"] != nil || fdef["x-constraint"] != nil || fdef["x-format"] != nil {
		return true
	}
	if fdef["maxLength"] != nil || fdef["maximum"] != nil || fdef["minLength"] != nil || fdef["minimum"] != nil {
		return true
	}
	if fdef["minItems"] != nil || fdef["maxItems"] != nil {
		return true
	}
	if fdef["enum"] != nil {
		return true
	}
	//oneOf -> values
	return false
}

func addAnnotation(anno map[rdl.ExtendedAnnotation]string, name string, value interface{}) map[rdl.ExtendedAnnotation]string {
	if value == nil {
		return anno
	}
	if anno == nil {
		anno = make(map[rdl.ExtendedAnnotation]string)
	}
	anno[rdl.ExtendedAnnotation(name)] = fmt.Sprint(value)
	return anno
}

func canonicalTypeName(tname string) string {
	switch tname {
	case "string":
		return "String"
	case "integer":
		return "Int32"
	case "number":
		return "Float64"
	case "boolean":
		return "Bool"
	case "object":
		return "Struct"
	case "array":
		return "Array"
	default:
		return tname
	}
}

//func normalizeTypeName(fdef swagger.Type) (string, string) {
func normalizeTypeName(fdef map[string]interface{}) (string, string) {
	fbase := "any"
	ftype := ""
	switch fdef["type"] {
	case "string":
		fbase = "String"
		ftype = fbase
	case "integer":
		fbase = "Int32"
		ftype = fbase
	case "number":
		fbase = "Float32"
		ftype = fbase
	case "boolean":
		fbase = "Bool"
		ftype = fbase
	case "object":
		fbase = "Struct"
		ftype = fbase
	case "array":
		fbase = "Array"
		ftype = fbase
	}
	ref := getString(fdef, "$ref")
	if strings.HasPrefix(ref, "#/definitions/") {
		ftype = ref[14:]
	}
	ftype = camelize(ftype)
	return ftype, fbase
}

func capitalize(text string) string {
	return strings.ToUpper(text[0:1]) + text[1:]
}

func camelize(raw string) string {
	switch raw {
	case "string":
		return "String"
	case "integer":
		return "Int32"
	case "number":
		return "Float64"
	case "array":
		return "Array"
	case "object":
		return "Struct"
	}
	lst := strings.Split(raw, " ")
	if len(lst) == 1 {
		return lst[0]
	}
	s := capitalize(lst[0])
	for _, ss := range lst[1:] {
		s = s + capitalize(ss)
	}
	return s
}

func pretty(obj interface{}) string {
	d, _ := json.MarshalIndent(obj, "", "    ")
	return string(d)
}
