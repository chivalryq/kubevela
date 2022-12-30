/*
Copyright 2021 The KubeVela Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package definition

import (
	"bytes"
	"fmt"
	"go/format"
	"io"
	"os"
	"strings"
	"unicode"

	"cuelang.org/go/cue"
	"github.com/fatih/camelcase"
	"github.com/pkg/errors"

	"github.com/oam-dev/kubevela/apis/types"
	velacue "github.com/oam-dev/kubevela/pkg/cue"
)

const (
	componentType    = "Component"
	traitType        = "Trait"
	workflowStepType = "WorkflowStep"
	policyType       = "Policy"
)

// StructParameter is a parameter that can be printed as a struct.
type StructParameter struct {
	types.Parameter
	// GoType is the same to parameter.Type but can be print in Go
	GoType string
	Fields []Field
}

// Field is a field of a struct.
type Field struct {
	Name    string
	JsonTag string
	// GoType is the same to parameter.Type but can be print in Go
	GoType    string
	OmitEmpty bool
	Param     *StructParameter
	Usage     string
}

type GenOption struct {
	SkipPackageName bool
	PackageName     string
	Prefix          string
	InputFile       string
	OutputFile      string
}

//nolint:gochecknoglobals
var (
	WellKnownAbbreviations = map[string]bool{
		"API":   true,
		"DB":    true,
		"HTTP":  true,
		"HTTPS": true,
		"ID":    true,
		"JSON":  true,
		"OS":    true,
		"SQL":   true,
		"SSH":   true,
		"URI":   true,
		"URL":   true,
		"XML":   true,
		"YAML":  true,

		"CPU": true,
		"PVC": true,
	}

	DefaultNamer = NewFieldNamer("")
)

// A FieldNamer generates a Go field name from a CUE label.
type FieldNamer interface {
	FieldName(label string) string
	SetPrefix(string)
}

// NewFieldNamer returns a new FieldNamer.
func NewFieldNamer(prefix string) FieldNamer {
	return &AbbrFieldNamer{Prefix: prefix, Abbreviations: WellKnownAbbreviations}
}

type Generator struct {
	// Name of component/trait/policy/workflow-step
	Name string
	// Kind can be "ComponentDefinition", "TraitDefinition", "PolicyDefinition", "WorkflowStepDefinition"
	Kind    string
	Structs []StructParameter

	// names that will be used to generate go code
	specName        string
	structName      string
	constructorName string
	receiverName    string
	typeVarName     string
}

// Run generates go code from cue file and print it.
func (g *Generator) Run(param cue.Value, option GenOption) error {
	err := g.GeneratorParameterStructs(param)
	if err != nil {
		return err
	}

	g.PrintDefinitions(option)
	return nil
}

// GeneratorParameterStructs generates structs for parameters in cue.
func (g *Generator) GeneratorParameterStructs(param cue.Value) error {
	g.Structs = []StructParameter{}
	_, err := g.parseParameters(param, specName(g.Name))
	return err
}

// NewStructParameter creates a StructParameter
func NewStructParameter() StructParameter {
	return StructParameter{
		Parameter: types.Parameter{},
		GoType:    "",
		Fields:    []Field{},
	}
}

// parseParameters will be called recursively to parse parameters
// nolint:staticcheck
func (g *Generator) parseParameters(paraValue cue.Value, paramKey string) (*StructParameter, error) {
	param := NewStructParameter()
	param.Name = paramKey
	param.Type = paraValue.IncompleteKind()
	param.Short, param.Usage, param.Alias, param.Ignore = velacue.RetrieveComments(paraValue)
	if def, ok := paraValue.Default(); ok && def.IsConcrete() {
		param.Default = velacue.GetDefault(def)
	}

	// only StructKind will be separated go struct, other will be just a field
	if param.Type == cue.StructKind {
		arguments, err := paraValue.Struct()
		if err != nil {
			return nil, fmt.Errorf("augument not as struct: %w", err)
		}
		if arguments.Len() == 0 { // in cue, empty struct like: foo: map[string]int
			tl := paraValue.Template()
			if tl != nil { // map type
				// TODO: kind maybe not simple type like string/int, if it is a struct, parseParameters should be called
				kind, err := trimIncompleteKind(tl("").IncompleteKind().String())
				if err != nil {
					return nil, errors.Wrap(err, "invalid parameter kind")
				}
				param.GoType = fmt.Sprintf("map[string]%s", kind)
			}
		}
		for i := 0; i < arguments.Len(); i++ {
			var field Field
			fi := arguments.Field(i)
			if fi.IsDefinition {
				continue
			}
			val := fi.Value
			name := fi.Selector
			field.Name = DefaultNamer.FieldName(name)
			field.JsonTag = name
			field.OmitEmpty = fi.IsOptional
			switch val.IncompleteKind() {
			case cue.StructKind:
				if subField, err := val.Struct(); err == nil && subField.Len() == 0 { // err cannot be not nil,so ignore it
					if mapValue, ok := val.Elem(); ok {
						// In the future we could recursively call to support complex map-value(struct or list)
						field.GoType = fmt.Sprintf("map[string]%s", mapValue.IncompleteKind().String())
					} else {
						// element in struct not defined, use interface{}
						field.GoType = "map[string]interface{}"
					}
				} else {
					subParam, err := g.parseParameters(val, name)
					if err != nil {
						return nil, err
					}
					field.GoType = DefaultNamer.FieldName(name)
					field.Param = subParam
				}
			case cue.ListKind:
				elem, success := val.Elem()
				if !success {
					// fail to get elements, use the value of ListKind to be the type
					field.GoType = normalizeGoType(val.IncompleteKind().String())
					break
				}
				switch elem.Kind() {
				case cue.StructKind:
					subParam, err := g.parseParameters(elem, name)
					if err != nil {
						return nil, err
					}
					field.GoType = fmt.Sprintf("[]%s", DefaultNamer.FieldName(name))
					field.Param = subParam
				default:
					field.GoType = fmt.Sprintf("[]%s", elem.IncompleteKind().String())
				}
			default:
				// todo
				_, usage, _, _ := velacue.RetrieveComments(val)
				field.GoType = normalizeGoType(val.IncompleteKind().String())
				field.Usage = usage
			}
			param.Fields = append(param.Fields, field)
		}
	}
	g.Structs = append(g.Structs, param)
	return &param, nil
}

// GenGoCodeFromParams generates go code from parameters
func GenGoCodeFromParams(parameters []StructParameter) (string, error) {
	var buf bytes.Buffer

	for _, parameter := range parameters {
		if parameter.Usage == "" {
			parameter.Usage = "-"
		}
		fmt.Fprintf(&buf, "// %s %s\n", DefaultNamer.FieldName(parameter.Name), parameter.Usage)
		genField(parameter, &buf)
	}
	source, err := format.Source(buf.Bytes())
	if err != nil {
		fmt.Println("Failed to format source:", err)
	}

	return string(source), nil
}

type PrintStep func(w io.Writer, option GenOption)

func (g *Generator) printBoilerplate(w io.Writer, option GenOption) {
	fmt.Fprintf(w, `
/*
Copyright 2023 The KubeVela Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
`)
	fmt.Fprintf(w, "// Code generated from %s using `vela def gen-api`. DO NOT EDIT.\n", option.InputFile)
	fmt.Fprintf(w, "\n")
}

func (g *Generator) printPackage(w io.Writer, option GenOption) {
	if !option.SkipPackageName {
		fmt.Fprintf(w, "package %s\n", option.PackageName)
	}
}

func (g *Generator) printImports(w io.Writer, _ GenOption) {
	fmt.Fprintf(w, `import (
	"github.com/oam-dev/kubevela-core-api/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela-core-api/pkg/oam/util"
	. "vela-go-sdk/api"
)
`)
}

func (g *Generator) printTypeVar(w io.Writer, _ GenOption) {
	fmt.Fprintf(w, `
const %s = "%s"
`, g.typeVarName, g.Name)
}

func (g *Generator) printRootStruct(w io.Writer, _ GenOption) {
	kind := normalizeKind(g.Kind)
	fmt.Fprintf(w, `// %s is the root struct of %s
type %s struct {
    Base %sBase
    Props %s
}
`, g.structName, g.Name, g.structName, kind, g.specName)
}

func (g *Generator) printPropertiesStructs(w io.Writer, _ GenOption) {
	for _, parameter := range g.Structs {
		if parameter.Usage == "" {
			parameter.Usage = "-"
		}
		fmt.Fprintf(w, "// %s %s\n", DefaultNamer.FieldName(parameter.Name), parameter.Usage)
		genField(parameter, w)
	}
}

func (g *Generator) printConstructorFunc(w io.Writer, _ GenOption) {
	defType := normalizeKind(g.Kind)
	switch defType {
	case componentType:
		fmt.Fprintf(w, `
func %s(name string) *%s {
    %s := %s{
        Base: %sBase{
            Name: name,
        },
    }
    return &%s
}
`, g.constructorName, g.structName, g.Name, g.structName, defType, g.Name)
	case traitType:
		fmt.Fprintf(w, `
func %s() *%s {
    %s := %s{
        Base: %sBase{},
    }
    return &%s
}
`, g.constructorName, g.structName, g.Name, g.structName, defType, g.Name)
	}

}

func (g *Generator) printTraitFunc(w io.Writer, _ GenOption) {
	if g.Kind != "ComponentDefinition" {
		return
	}
	fmt.Fprintf(w, `
%s Trait(traits ...Trait) *%s {
    %s.Base.Traits = append(%s.Base.Traits, traits...)
    return %s
}
`, g.funcReceiver(), g.structName, g.receiverName, g.receiverName, g.receiverName)
}

func (g *Generator) printBuildFunc(w io.Writer, option GenOption) {
	switch g.Kind {
	case "ComponentDefinition":
		g.printBuildFuncForComponent(w, option)
	case "TraitDefinition":
		g.printBuildFuncForTrait(w, option)
	case "PolicyDefinition":
		//g.printBuildFuncForPolicy(w,option)
	case "WorkflowStepDefinition":
		//g.printBuildFuncForWorkflowStep(w,option)
	}
}

func (g *Generator) printBuildFuncForComponent(w io.Writer, _ GenOption) {
	fmt.Fprintf(w, `
%s Build() common.ApplicationComponent {
    traits := make([]common.ApplicationTrait, 0)
    for _, trait := range %s.Base.Traits {
    	traits = append(traits, trait.Build())
    }
    comp := common.ApplicationComponent{
    	Name:       %s.Base.Name,
    	Type:       %s,
    	Properties: util.Object2RawExtension(%s.Props),
    	DependsOn:  %s.Base.DependsOn,
    	Inputs:     %s.Base.Inputs,
    	Outputs:    %s.Base.Outputs,
    	Traits:     traits,
    }
    return comp
}
`, g.funcReceiver(), g.receiverName, g.receiverName, g.typeVarName, g.receiverName, g.receiverName, g.receiverName, g.receiverName)
}

func (g *Generator) printBuildFuncForTrait(w io.Writer, _ GenOption) {
	fmt.Fprintf(w, `
%s Build() common.ApplicationTrait {
    trait := common.ApplicationTrait {
        Type:       %s,
        Properties: util.Object2RawExtension(%s.Props),
    }
    return trait
}
`, g.funcReceiver(), g.typeVarName, g.receiverName)
}

func (g *Generator) printPropertiesFunc(w io.Writer, _ GenOption) {
	var topLevelFields []Field
	for _, parameter := range g.Structs {
		if parameter.Name == g.specName {
			topLevelFields = parameter.Fields
		}
	}

	for _, field := range topLevelFields {
		var usage string
		if field.Param != nil {
			usage = field.Param.Usage
		}
		if usage == "" {
			usage = "-"
		}
		fmt.Fprintf(w, "// %s %s\n", field.Name, usage)
		fmt.Fprintf(w, `%s %s(value %s) *%s {
    %s.Props.%s = value
    return %s
}
`, g.funcReceiver(), field.Name, field.GoType, g.structName, g.receiverName, field.Name, g.receiverName)
	}
}

func (g *Generator) funcReceiver() string {
	return fmt.Sprintf("func (%s *%s)", g.receiverName, g.structName)
}

// PrintDefinitions prints the StructParameter in Golang struct format
func (g *Generator) PrintDefinitions(option GenOption) {
	g.initNames()
	writer := os.Stdout
	if option.OutputFile != "" {
		file, err := os.Create(option.OutputFile)
		if err != nil {
			panic(err)
		}
		writer = file
	}

	buf := &bytes.Buffer{}
	steps := []PrintStep{
		g.printBoilerplate,
		g.printPackage,
		g.printImports,
		g.printTypeVar,
		g.printRootStruct,
		g.printPropertiesStructs,
		g.printConstructorFunc,
		g.printTraitFunc,
		g.printBuildFunc,
		g.printPropertiesFunc,
	}
	for _, step := range steps {
		step(buf, option)
	}

	source, err := format.Source(buf.Bytes())
	if err != nil {
		fmt.Println("Failed to format source:", err)
	}
	_, _ = writer.Write(source)
}

func (g *Generator) initNames() {
	g.specName = specName(g.Name)
	g.structName = structName(g.Name, g.Kind)
	g.constructorName = constructorName(g.Name)
	g.typeVarName = typeVarName(g.Name)
	g.receiverName = receiverName(g.Name)
}

func genField(param StructParameter, writer io.Writer) {
	fieldName := DefaultNamer.FieldName(param.Name)
	if param.Type == cue.StructKind { // only struct kind will be separated struct
		// cue struct  can be Go map or struct
		if strings.HasPrefix(param.GoType, "map[string]") {
			fmt.Fprintf(writer, "type %s %s", fieldName, param.GoType)
		} else {
			fmt.Fprintf(writer, "type %s struct {\n", fieldName)
			for _, f := range param.Fields {
				jsonTag := f.JsonTag
				if f.OmitEmpty {
					jsonTag = fmt.Sprintf("%s,omitempty", jsonTag)
				}
				fmt.Fprintf(writer, "    %s %s `json:\"%s\"`\n", f.Name, f.GoType, jsonTag)
			}

			fmt.Fprintf(writer, "}\n")
		}
	} else {
		fmt.Fprintf(writer, "type %s %s\n", fieldName, param.GoType)
	}
}

// trimIncompleteKind allows 2 types of incomplete kind, return the non-null one, more than two types of incomplete kind will return error
// 1. (null|someKind)
// 2. someKind
func trimIncompleteKind(mask string) (string, error) {
	mask = strings.Trim(mask, "()")
	ks := strings.Split(mask, "|")
	if len(ks) == 1 {
		return ks[0], nil
	}
	if len(ks) == 2 && ks[0] == "null" {
		return ks[1], nil
	}
	return "", fmt.Errorf("invalid incomplete kind: %s", mask)

}

// An AbbrFieldNamer generates Go field names from Go
// struct field while keeping abbreviations uppercased.
type AbbrFieldNamer struct {
	// Prefix is a prefix to add to all field names with first char capitalized automatically.
	Prefix                         string
	prefixWithFirstCharCapitalized string
	Abbreviations                  map[string]bool
}

// SetPrefix set a prefix to namer.
func (a *AbbrFieldNamer) SetPrefix(s string) {
	a.Prefix = s
}

// FieldName implements FieldNamer.FieldName.
func (a *AbbrFieldNamer) FieldName(field string) string {
	if a.prefixWithFirstCharCapitalized == "" && a.Prefix != "" {
		a.prefixWithFirstCharCapitalized = strings.ToUpper(a.Prefix[:1]) + a.Prefix[1:]
	}
	components := SplitComponents(field)
	for i, component := range components {
		switch {
		case component == "":
			// do nothing
		case a.Abbreviations[strings.ToUpper(component)]:
			components[i] = strings.ToUpper(component)
		case component == strings.ToUpper(component):
			runes := []rune(component)
			components[i] = string(runes[0]) + strings.ToLower(string(runes[1:]))
		default:
			runes := []rune(component)
			runes[0] = unicode.ToUpper(runes[0])
			components[i] = string(runes)
		}
	}
	runes := []rune(strings.Join(components, ""))
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			runes[i] = '_'
		}
	}
	fieldName := string(runes)
	if !unicode.IsLetter(runes[0]) && runes[0] != '_' {
		fieldName = "_" + fieldName
	}
	if a.prefixWithFirstCharCapitalized != "" {
		fieldName = a.prefixWithFirstCharCapitalized + fieldName
	}
	return fieldName
}

// SplitComponents splits name into components. name may be kebab case, snake
// case, or camel case.
func SplitComponents(name string) []string {
	switch {
	case strings.ContainsRune(name, '-'):
		return strings.Split(name, "-")
	case strings.ContainsRune(name, '_'):
		return strings.Split(name, "_")
	default:
		return camelcase.Split(name)
	}
}

func specName(definitionName string) string {
	return DefaultNamer.FieldName(definitionName + "Spec")
}

func constructorName(definitionName string) string {
	return DefaultNamer.FieldName(definitionName)
}
func structName(definitionName, definitionKind string) string {
	return DefaultNamer.FieldName(definitionName) + normalizeKind(definitionKind)
}

func typeVarName(definitionName string) string {
	return definitionName + "Type"
}

func receiverName(definitionName string) string {
	return definitionName[:1]
}

func normalizeKind(definitionKind string) string {
	return strings.TrimSuffix(definitionKind, "Definition")
}

func normalizeGoType(goType string) string {
	if goType == "number" {
		return "float64"
	}
	return goType
}
