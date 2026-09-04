// Package workspace defines the schema-version-2 workspace domain.
//
// The package deliberately does not read a checkout or execute commands while
// validating a definition. Callers resolve source bytes, then pass those
// immutable inputs to ValidateDefinition. Version-1 plan parsing remains in
// internal/plan and is neither imported nor accepted as workspace input.
//
// Owner and reviewer identifiers are descriptive labels in a local workflow;
// they are not authenticated identities. Journal record hashes and the hash
// chain detect corruption of locally stored history, but do not authenticate
// who created a record.
package workspace
