package main

import (
	"bytes"
	"strings"
	"text/template"

	"github.com/rancher/rancher-compose-executor/template/funcs"
)

func ApplyTemplating(contents []byte) ([]byte, error) {
	// Skip templating if contents begin with '# notemplating'
	trimmedContents := strings.TrimSpace(string(contents))
	if strings.HasPrefix(trimmedContents, "#notemplating") || strings.HasPrefix(trimmedContents, "# notemplating") {
		return contents, nil
	}

	t, err := template.New("template").Option("missingkey=zero").Funcs(funcs.Funcs).Parse(string(contents))
	if err != nil {
		return nil, err
	}

	buf := bytes.Buffer{}
	err = t.Execute(&buf, map[string]interface{}{
		"Values": make(map[string]string),
		// "Release": nil,
		// "Stack":   nil,
		// "Cluster": nil,
	})
	return buf.Bytes(), err
}
