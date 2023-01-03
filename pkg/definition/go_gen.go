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
	"github.com/kubevela/pkg/util/slices"
	"github.com/kubevela/workflow/pkg/cue/model/value"
	"github.com/kubevela/workflow/pkg/cue/packages"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
	// ReversedPaths is the path to this parameter which the order is reversed. It could be used to solve the problem of name conflict.
	ReversedPaths []string
	// the index of the path that is a pointer
	PathPtr int
}

// Field is a field of a struct.
type Field struct {
	Name      string
	JsonTag   string
	OmitEmpty bool
	Param     *StructParameter
	Usage     string
	GetGoType GoTypeGetter
}

type GoTypeGetter func() string

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
		"CPU":   true,
		"PVC":   true,
		"IP":    true,
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
	Structs map[string]*StructParameter

	// reversedPaths is the reversed path now generator locates when traversing the cue value.
	reversedPaths []string

	// names that will be used to generate go code
	specName        string
	structName      string
	constructorName string
	receiverName    string
	typeVarName     string
}

// Run generates go code from cue file and print it.
func (g *Generator) Run(template string, option GenOption, pd *packages.PackageDiscover) error {
	g.initNames()
	values, err := getNecessaryValues(template, pd)
	if err != nil {
		return err
	}
	err = g.GenerateParameterStructs(values)
	if err != nil {
		return err
	}

	return g.PrintDefinitions(option)
}

// GenerateParameterStructs generates structs for parameters in cue.
func (g *Generator) GenerateParameterStructs(vals []cue.Value) error {
	g.Structs = make(map[string]*StructParameter)
	// todo #HealthProbe is not in paramter value, select definition and add it to parse process.
	for _, v := range vals {
		l, ok := v.Label()
		if !ok {
			return errors.New("get label failed")
		}
		switch {
		case l == "parameter":
			_, err := g.parseParameters(v, g.specName)
			if err != nil {
				return errors.Wrap(err, "parse parameters")
			}
		case strings.HasPrefix(l, "#"):
			_, err := g.parseParameters(v, strings.TrimPrefix(l, "#"))
			if err != nil {
				return errors.Wrap(err, "parse definition")
			}
		}
	}
	return nil
}

func getNecessaryValues(template string, pd *packages.PackageDiscover) ([]cue.Value, error) {
	tmpl, err := value.NewValue(template+velacue.BaseTemplate, pd, "")
	if err != nil {
		return []cue.Value{}, err
	}
	// We need the parameter field and all field that is definition
	iter, err := tmpl.CueValue().Fields(cue.All())
	if err != nil {
		return []cue.Value{}, errors.Wrap(err, "iterate fields")
	}
	var values []cue.Value
	for iter.Next() {
		if iter.Label() == "parameter" {
			values = append(values, iter.Value())
		}
		if strings.HasPrefix(iter.Label(), "#") {
			values = append(values, iter.Value())
		}
	}
	return values, nil
}

// NewStructParameter creates a StructParameter
func NewStructParameter(paths []string) StructParameter {
	copiedPaths := make([]string, len(paths))
	copy(copiedPaths, paths)
	return StructParameter{
		Parameter:     types.Parameter{},
		GoType:        "",
		Fields:        []Field{},
		PathPtr:       0,
		ReversedPaths: copiedPaths,
	}
}

// parseParameters will be called recursively to parse parameters
// nolint:staticcheck
func (g *Generator) parseParameters(paraValue cue.Value, paramKey string) (*StructParameter, error) {

	g.reversedPaths = append([]string{paramKey}, g.reversedPaths...)

	param := NewStructParameter(g.reversedPaths)
	param.Name = DefaultNamer.FieldName(paramKey)
	param.Type = paraValue.IncompleteKind()
	param.Short, param.Usage, param.Alias, param.Ignore = velacue.RetrieveComments(paraValue)
	if def, ok := paraValue.Default(); ok && def.IsConcrete() {
		param.Default = velacue.GetDefault(def)
	}

	// only StructKind will be separated go struct, other will be just a field
	if param.Type == cue.StructKind {
		param.GoType = param.Name
		fi, err := paraValue.Fields(cue.All())
		if err != nil {
			return nil, fmt.Errorf("augument not as struct: %w", err)
		}
		zeroFlag := true
		for fi.Next() {
			if fi.Selector().IsDefinition() {
				continue
			}
			zeroFlag = false
			var field Field
			val := fi.Value()
			name := fi.Selector().String()
			_, usage, _, _ := velacue.RetrieveComments(val)
			field.Name = DefaultNamer.FieldName(name)
			field.JsonTag = name
			field.OmitEmpty = fi.IsOptional()
			field.Usage = usage
			// The whole switch block is to get field.GoType. For doing this we can
			// 1. For types which is not associated with a go type,
			// 2. For types which is associated with a go type, use goTypeParam and goTypeParamList to enclose the sub-param
			switch val.IncompleteKind() {
			case cue.StructKind:
				if subField, err := val.Struct(); err == nil && subField.Len() == 0 { // err cannot be not nil,so ignore it
					if mapValue, ok := val.Elem(); ok {
						// In the future we could recursively call to support complex map-value(struct or list)
						ik := mapValue.IncompleteKind()
						switch ik {
						case cue.StructKind:
							_structName, inlineStruct, err := g.parseStruct(mapValue, name+"Item")
							if err != nil {
								return nil, err
							}
							setFieldGoTypeWithNameOrStruct(&field, _structName, inlineStruct, goTypeOrigin, goTypeParamMap)
						case cue.ListKind:
							return nil, errors.New("map value is list not supported")
						default:
							field.GetGoType = goTypeFixed(fmt.Sprintf("map[string]%s", ik.String()))
						}
					} else {
						// element in struct not defined, consider it as any struct.
						field.GetGoType = goTypeFixed("map[string]interface{}")
					}
				} else {
					_structName, inlineStruct, err := g.parseStruct(val, name)
					if err != nil {
						return nil, err
					}
					setFieldGoTypeWithNameOrStruct(&field, _structName, inlineStruct, goTypeOrigin, goTypeParam)
				}
			case cue.ListKind:
				elem, success := val.Elem()
				if !success {
					// fail to get elements, use the value of ListKind to be the type
					// todo this isn't right, the result can be "type foo list"
					field.GetGoType = goTypeFixed("[]" + normalizeGoType(elem.IncompleteKind().String()))
					break
				}
				switch elem.Kind() {
				case cue.StructKind:
					_structName, inlineStruct, err := g.parseStruct(elem, name)
					if err != nil {
						return nil, err
					}
					setFieldGoTypeWithNameOrStruct(&field, _structName, inlineStruct, goTypeListRefDef, goTypeParamList)
				default:
					field.GetGoType = goTypeFixed(fmt.Sprintf("[]%s", elem.IncompleteKind().String()))
				}
			default:
				// IncompleteKind is a mask. If it's not a StructKind or ListKind, it can be
				// 1. a concrete type, like int, string, etc.
				// 2. a combined type, like int|float, string|bool, etc.
				field.GetGoType = goTypeFixed(normalizeGoType(val.IncompleteKind().String()))
			}
			param.Fields = append(param.Fields, field)
		}

		if zeroFlag { // in cue, empty struct like: foo: map[string]int
			tl := paraValue.Template()
			if tl != nil { // map type
				// TODO: kind maybe not simple type like string/int, if it is a struct, parseParameters should be called
				kind, err := trimIncompleteKind(tl("").IncompleteKind().String())
				if err != nil {
					return nil, errors.Wrap(err, "invalid parameter kind")
				}
				param.GoType = fmt.Sprintf("map[string]%s", kind)
			}
			// todo check what if tl is nil
			//} else {
			//	// map value is a struct
			//	return nil, errors.New("not support map value is a struct")
			//
			//}
		}
	} else {
		param.GoType = normalizeGoType(param.Type.String())
	}
	addedParam, err := g.addStructsAndDetectConflict(&param)
	if err != nil {
		return nil, errors.Wrap(err, "add structs")
	}

	g.reversedPaths = g.reversedPaths[1:]

	return addedParam, nil
}

// parseStruct helps parse a struct. It returns:
// 1. struct name if struct refers to a definition, else empty string
// 2. struct pointer if struct is defined inline, else nil
// 3. error if any
func (g *Generator) parseStruct(val cue.Value, name string) (string, *StructParameter, error) {
	_, p := val.ReferencePath()
	sels := p.Selectors()
	if sels != nil {
		// Reference to a struct definition
		defNameWithoutSharp := strings.TrimPrefix(sels[len(sels)-1].String(), "#")
		return DefaultNamer.FieldName(defNameWithoutSharp), nil, nil
	} else {
		subParam, err := g.parseParameters(val, name)
		if err != nil {
			return "", nil, err
		}
		return "", subParam, nil
	}

}

// cmpStruct is the StructParameter without StructParameter.ReversedPaths and StructParameter.PathPtr
type cmpStruct struct {
	types.Parameter
	// GoType is the same to parameter.Type but can be print in Go
	GoType string
	Fields []Field
}

func (g *Generator) addStructsAndDetectConflict(sNew *StructParameter) (*StructParameter, error) {
	sOld, ok := g.Structs[sNew.Name]
	if !ok {
		g.Structs[sNew.Name] = sNew
		return sNew, nil
	}
	// compare the structs except the paths
	// If they are deep equal, just skip adding new struct
	// Else add paths to the both struct name to distinguish them
	if reflect.DeepEqual(cmpStruct{
		Parameter: sOld.Parameter,
		GoType:    sOld.GoType,
		Fields:    sOld.Fields,
	}, cmpStruct{
		Parameter: sNew.Parameter,
		GoType:    sNew.GoType,
		Fields:    sNew.Fields,
	}) {
		return sOld, nil
	} else {
		// if the struct is the same, add one more path to both of them
		oldKey := sOld.Name
	AddPrefix:
		for {
			for _, s := range []*StructParameter{sOld, sNew} {
				if s.PathPtr < len(s.ReversedPaths)-1 {
					s.PathPtr++
					s.Name = DefaultNamer.FieldName(s.ReversedPaths[s.PathPtr]) + s.Name
					s.GoType = s.Name
				} else {
					return nil, errors.Errorf("fail to add struct, name conflict: %s", s.GoType)
				}
			}
			if sOld.Name != sNew.Name {
				g.Structs[sOld.Name] = sOld
				g.Structs[sNew.Name] = sNew
				break AddPrefix
			}
		}
		delete(g.Structs, oldKey)
	}
	return sNew, nil
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
		p := normalizePackageName(option.PackageName)
		fmt.Fprintf(w, "package %s\n", p)
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
		genField(*parameter, w)
	}
}

func (g *Generator) printConstructorFunc(w io.Writer, _ GenOption) {
	defType := normalizeKind(g.Kind)
	switch defType {
	case componentType:
		fmt.Fprintf(w, `
func %s(name string) *%s {
    comp := %s{
        Base: %sBase{
            Name: name,
        },
    }
    return &comp
}
`, g.constructorName, g.structName, g.structName, defType)
	case traitType:
		fmt.Fprintf(w, `
func %s() *%s {
    trait := %s{
        Base: %sBase{},
    }
    return &trait
}
`, g.constructorName, g.structName, g.structName, defType)
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
		if parameter.GoType == g.specName {
			topLevelFields = parameter.Fields
		}
	}

	for _, field := range topLevelFields {
		usage := field.Usage
		if usage == "" {
			usage = "-"
		}
		fmt.Fprintf(w, "// %s %s\n", field.Name, usage)
		fmt.Fprintf(w, `%s %s(value %s) *%s {
    %s.Props.%s = value
    return %s
}
`, g.funcReceiver(), field.Name, field.GetGoType(), g.structName, g.receiverName, field.Name, g.receiverName)
	}
}

func (g *Generator) funcReceiver() string {
	return fmt.Sprintf("func (%s *%s)", g.receiverName, g.structName)
}

// PrintDefinitions prints the StructParameter in Golang struct format
func (g *Generator) PrintDefinitions(option GenOption) error {
	writer := os.Stdout
	if option.OutputFile != "" {
		// Create the whole path to file and create file
		if err := os.MkdirAll(filepath.Dir(option.OutputFile), 0755); err != nil {
			panic(err)
		}
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
		return errors.Wrap(err, "format source")
	}
	_, err = writer.Write(source)
	return err
}

func (g *Generator) initNames() {
	g.specName = specName(g.Name)
	g.structName = structName(g.Name, g.Kind)
	g.constructorName = constructorName(g.Name)
	g.typeVarName = typeVarName(g.Name)
	g.receiverName = receiverName(g.Name)
}

func genField(param StructParameter, writer io.Writer) {
	fieldName := param.Name
	fmt.Fprintf(writer, "// %s %s\n", fieldName, param.Usage)
	if param.Type == cue.StructKind { // only struct kind will be separated struct
		// cue struct  can be Go map or struct
		if strings.HasPrefix(param.GoType, "map[string]") {
			fmt.Fprintf(writer, "type %s %s", fieldName, param.GoType)
		} else {
			if len(param.Fields) == 0 {
				// empty struct, use interface{}
				fmt.Fprintf(writer, "type %s interface{}\n", fieldName)
			} else {
				fmt.Fprintf(writer, "type %s struct {\n", fieldName)
				for _, f := range param.Fields {
					jsonTag := f.JsonTag
					if f.OmitEmpty {
						jsonTag = fmt.Sprintf("%s,omitempty", jsonTag)
					}
					fmt.Fprintf(writer, "    %s %s `json:\"%s\"`\n", f.Name, f.GetGoType(), jsonTag)
				}
				fmt.Fprintf(writer, "}\n")
			}
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
	// Do some replacing to avoid split k8s to three parts ["k", "8", "s"].
	// Todo(chivalryq) this may wrongly make original "Kubernetes" become "k8s"
	field = strings.ReplaceAll(field, "k8s", "Kubernetes")
	field = strings.ReplaceAll(field, "K8s", "Kubernetes")
	components := SplitComponents(field)
	components = slices.Map(components, func(s string) string {
		if s == "Kubernetes" {
			return "K8s"
		}
		return s
	})
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
	return DefaultNamer.FieldName(definitionName) + "Spec"
}

func constructorName(definitionName string) string {
	return DefaultNamer.FieldName(definitionName)
}
func structName(definitionName, definitionKind string) string {
	return DefaultNamer.FieldName(definitionName) + normalizeKind(definitionKind)
}

func typeVarName(definitionName string) string {
	return DefaultNamer.FieldName(definitionName) + "Type"
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

func normalizePackageName(packageName string) string {
	return strings.ReplaceAll(packageName, "-", "_")
}

func goTypeFixed(goType string) func() string {
	return func() string {
		return goType
	}
}

func goTypeOrigin(goType string) string {
	return goType
}

func goTypeListRefDef(goType string) string {
	return "[]" + goType
}

func goTypeParam(param *StructParameter) string {
	return param.GoType
}

func goTypeParamList(param *StructParameter) string {
	return "[]" + param.GoType
}

func goTypeParamMap(param *StructParameter) string {
	return "map[string]" + param.GoType
}

func setFieldGoTypeWithNameOrStruct(field *Field, name string, goType *StructParameter, nameConverter func(string) string, goTypeConverter func(parameter *StructParameter) string) {
	if name != "" {
		field.GetGoType = func() string {
			return nameConverter(name)
		}
	}
	if goType != nil {
		field.GetGoType = func() string {
			return goTypeConverter(goType)
		}
	}
}
