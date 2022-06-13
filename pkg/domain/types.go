package domain

import (
	"bytes"
	"text/template"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	IfNotExistsRule       = "IfNotExists"
	ManagedVariableName   = "managed"
	DependentVariableName = "dependent"
)

type GroupVersionKind struct {
	Group   string `json:"group,omitempty"`
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

type DeletionBlockRule struct {
	Type      string           `json:"type,omitempty"`
	Query     string           `json:"query,omitempty"`
	Managed   GroupVersionKind `json:"managed,omitempty"`
	Dependent GroupVersionKind `json:"dependent,omitempty"`
}

func (dbr DeletionBlockRule) CheckIsDeletionAllowed(managed unstructured.Unstructured, dependents unstructured.UnstructuredList) (bool, error) {
	if dbr.Type == IfNotExistsRule {
		for _, d := range dependents.Items {
			allowed, err := dbr.CheckIsDeletionAllowedForASingleDependent(managed, d)
			if err != nil || allowed == false {
				return false, err
			}
		}
	}

	return true, nil
}

func (dbr DeletionBlockRule) CheckIsDeletionAllowedForASingleDependent(managed unstructured.Unstructured, dependent unstructured.Unstructured) (bool, error) {
	tmpl, err := template.New("rule").Parse(dbr.Query)
	if err != nil {
		return false, err
	}

	data := map[string]interface{}{
		ManagedVariableName:   managed.Object,
		DependentVariableName: dependent.Object,
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, data)
	if err != nil {
		return false, err
	}
	result := buf.String()

	return result != "true", nil
}

func (k GroupVersionKind) GetSchemaGroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   k.Group,
		Version: k.Version,
		Kind:    k.Kind,
	}
}
