package extracttest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// YAMLNodes counts the nodes yaml.v3 builds from data, document nodes too,
// or -1 when it does not parse.
func YAMLNodes(data []byte) int {
	var count func(*yaml.Node) int
	count = func(n *yaml.Node) int {
		c := 1
		for _, k := range n.Content {
			c += count(k)
		}
		return c
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	total := 0
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return total
			}
			return -1
		}
		total += count(&doc)
	}
}

// OpenAPI is an OpenAPI document of n paths of three operations each.
func OpenAPI(n int) string {
	op := func(i, m string) string {
		return "    " + m + ":\n      summary: Operation " + m + " on resource " + i + "\n      operationId: " + m + "Resource" + i +
			"\n      parameters:\n        - name: limit\n          in: query\n          required: false\n          schema:\n            type: integer\n            format: int32\n" +
			"      responses:\n        '200':\n          description: A paged array of resources\n          content:\n            application/json:\n              schema:\n                $ref: '#/components/schemas/Resource'\n        default:\n          description: unexpected error\n"
	}
	return "openapi: 3.0.0\ninfo:\n  title: x\n  version: 1.0.0\npaths:\n" + RepeatLines(n, func(i int) string {
		s := strconv.Itoa(i)
		return "  /resource" + s + ":\n" + op(s, "get") + op(s, "put") + op(s, "delete")
	})
}

// HelmIndex is a Helm chart repository's index.yaml: charts of n versions.
func HelmIndex(charts, n int) string {
	return "apiVersion: v1\nentries:\n" + RepeatLines(charts, func(c int) string {
		return fmt.Sprintf("  chart%d:\n", c) + RepeatLines(n, func(v int) string {
			return fmt.Sprintf(`  - annotations:
      artifacthub.io/license: Apache-2.0
    apiVersion: v2
    appVersion: v%[2]d.0.0
    created: "2026-01-01T00:00:00.000000000Z"
    dependencies:
    - condition: crds.enabled
      name: crds
      repository: ""
      version: 0.0.0
    description: A chart that runs one service of the cluster
    digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    home: https://example.com/chart%[1]d
    keywords:
    - monitoring
    - metrics
    maintainers:
    - email: someone@example.com
      name: someone
    name: chart%[1]d
    sources:
    - https://example.com/src
    type: application
    urls:
    - https://example.com/charts/chart%[1]d-%[2]d.0.0.tgz
    version: %[2]d.0.0
`, c, v)
		})
	}) + "generated: \"2026-10-17T00:00:00Z\"\n"
}

// UVLock is a uv.lock of n packages of w wheels.
func UVLock(n, w int) string {
	return "version = 1\nrequires-python = \">=3.9\"\n\n" + RepeatLines(n, func(i int) string {
		return fmt.Sprintf("[[package]]\nname = \"p%d\"\nversion = \"1.0\"\nsource = { registry = \"https://pypi.org/simple\" }\n"+
			"dependencies = [\n    { name = \"a\" },\n    { name = \"b\" },\n]\nsdist = { url = \"https://x/p.tar.gz\", hash = \"sha256:ab\", size = 1 }\nwheels = [\n", i) +
			RepeatLines(w, func(j int) string {
				return fmt.Sprintf("    { url = \"https://x/p%d.whl\", hash = \"sha256:ab\", size = %d },\n", j, j)
			}) +
			"]\n\n[package.optional-dependencies]\nx = [\n    { name = \"c\" },\n]\n\n"
	})
}
