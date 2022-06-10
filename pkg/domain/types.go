package domain

import (
	"bytes"
	"text/template"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func (dbr DeletionBlockRule) CheckIsDeletionAllowed(managed unstructured.Unstructured, dependents unstructured.UnstructuredList) bool {
	if dbr.Type == "IfNotExists" {
		for _, d := range dependents.Items {
			if !dbr.CheckIsDeletionAllowedForASingleDependent(managed, d) {
				return false
			}
		}
	}

	return true
}

func (dbr DeletionBlockRule) CheckIsDeletionAllowedForASingleDependent(managed unstructured.Unstructured, dependent unstructured.Unstructured) bool {
	tmpl, err := template.New("rule").Parse(dbr.Query)
	if err != nil {
		panic(err)
	}
	data := map[string]interface{}{
		"managed":   managed.Object,
		"dependent": dependent.Object,
	}
	var tpl bytes.Buffer
	err = tmpl.Execute(&tpl, data)
	if err != nil {
		panic(err)
	}
	result := tpl.String()

	return result == "true"
}

func (k GroupVersionKind) GetSchemaGroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   k.Group,
		Version: k.Version,
		Kind:    k.Kind,
	}
}
