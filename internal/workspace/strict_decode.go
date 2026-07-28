package workspace

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	MaxArtifactBytes = 1 << 20
	maxYAMLDepth     = 64
	maxYAMLNodes     = 100_000
)

var canonicalYAMLInteger = regexp.MustCompile(`^(?:0|[1-9][0-9]*)$`)

func decodeStrictV2(kind string, source []byte, target any) error {
	if len(source) == 0 {
		return fmt.Errorf("%s is empty", kind)
	}
	if len(source) > MaxArtifactBytes {
		return fmt.Errorf("%s exceeds %d bytes", kind, MaxArtifactBytes)
	}

	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode %s YAML: %w", kind, err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%s must contain exactly one YAML document", kind)
		}
		return fmt.Errorf("decode trailing %s YAML: %w", kind, err)
	}

	nodes := 0
	if err := validateYAMLNode(&document, 0, &nodes); err != nil {
		return fmt.Errorf("invalid %s YAML: %w", kind, err)
	}
	root, err := yamlRootMapping(&document)
	if err != nil {
		return fmt.Errorf("invalid %s YAML: %w", kind, err)
	}
	targetType := reflect.TypeOf(target)
	if targetType == nil || targetType.Kind() != reflect.Pointer || targetType.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("decode %s target must be a struct pointer", kind)
	}
	if err := validateYAMLTypes(root, targetType.Elem(), "root"); err != nil {
		return fmt.Errorf("invalid %s YAML: %w", kind, err)
	}
	version, present := mappingScalar(root, "schema_version")
	if !present {
		return fmt.Errorf("%s schema_version is required", kind)
	}
	if version != "2" {
		return fmt.Errorf("%s schema_version %s is not supported; v2 is required", kind, version)
	}

	strict := yaml.NewDecoder(bytes.NewReader(source))
	strict.KnownFields(true)
	if err := strict.Decode(target); err != nil {
		return fmt.Errorf("decode strict %s: %w", kind, err)
	}
	if err := strict.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%s must contain exactly one YAML document", kind)
	}
	return nil
}

func validateYAMLTypes(node *yaml.Node, target reflect.Type, location string) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}

	switch target.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s must be a mapping", location)
		}
		fields := make(map[string]reflect.Type, target.NumField())
		for index := 0; index < target.NumField(); index++ {
			field := target.Field(index)
			name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
			if name != "" && name != "-" {
				fields[name] = field.Type
			}
		}
		for index := 0; index < len(node.Content); index += 2 {
			name := node.Content[index].Value
			fieldType, exists := fields[name]
			if !exists {
				continue // KnownFields reports the unsupported field after type validation.
			}
			if err := validateYAMLTypes(node.Content[index+1], fieldType, location+"."+name); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if node.Kind != yaml.SequenceNode {
			return fmt.Errorf("%s must be a sequence", location)
		}
		for index, item := range node.Content {
			if err := validateYAMLTypes(item, target.Elem(), fmt.Sprintf("%s[%d]", location, index)); err != nil {
				return err
			}
		}
	case reflect.String:
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			return fmt.Errorf("%s must be a string", location)
		}
	case reflect.Bool:
		if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
			return fmt.Errorf("%s must be a boolean", location)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
			return fmt.Errorf("%s must be an integer", location)
		}
	default:
		return fmt.Errorf("%s uses unsupported destination type %s", location, target)
	}
	return nil
}

func validateYAMLNode(node *yaml.Node, depth int, count *int) error {
	if node == nil {
		return fmt.Errorf("nil YAML node")
	}
	(*count)++
	if *count > maxYAMLNodes {
		return fmt.Errorf("document exceeds %d nodes", maxYAMLNodes)
	}
	if depth > maxYAMLDepth {
		return fmt.Errorf("document exceeds nesting depth %d", maxYAMLDepth)
	}
	if node.Anchor != "" || node.Alias != nil || node.Kind == yaml.AliasNode {
		return fmt.Errorf("anchors and aliases are not supported")
	}

	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			return fmt.Errorf("document must contain one root node")
		}
	case yaml.MappingNode:
		if node.Tag != "!!map" || len(node.Content)%2 != 0 {
			return fmt.Errorf("only string-keyed mappings are supported")
		}
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Value == "<<" {
				return fmt.Errorf("YAML merge keys are not supported")
			}
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "" {
				return fmt.Errorf("mapping keys must be non-empty strings")
			}
			if _, exists := seen[key.Value]; exists {
				return fmt.Errorf("duplicate field %q", key.Value)
			}
			seen[key.Value] = struct{}{}
		}
	case yaml.SequenceNode:
		if node.Tag != "!!seq" {
			return fmt.Errorf("custom sequence tags are not supported")
		}
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!str":
		case "!!int":
			if !canonicalYAMLInteger.MatchString(node.Value) {
				return fmt.Errorf("integer %q must use unsigned decimal form", node.Value)
			}
		case "!!bool":
			if node.Value != "true" && node.Value != "false" {
				return fmt.Errorf("boolean %q must be true or false", node.Value)
			}
		default:
			return fmt.Errorf("scalar tag %q is not supported", node.Tag)
		}
	default:
		return fmt.Errorf("YAML node kind %d is not supported", node.Kind)
	}

	for _, child := range node.Content {
		if err := validateYAMLNode(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func yamlRootMapping(document *yaml.Node) (*yaml.Node, error) {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, fmt.Errorf("expected one YAML document")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("root must be a mapping")
	}
	return root, nil
}

func mappingScalar(mapping *yaml.Node, name string) (string, bool) {
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == name {
			return mapping.Content[index+1].Value, true
		}
	}
	return "", false
}
