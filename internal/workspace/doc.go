// Package workspace defines the schema-version-2 workspace domain.
//
// The package deliberately does not read a checkout or execute commands while
// validating a definition. Callers resolve source bytes and pinned authority
// material through narrow ports, then pass those immutable inputs to
// ValidateDefinition. Version-1 plan parsing remains in internal/plan and is
// neither imported nor accepted as workspace-v2 input.
package workspace
