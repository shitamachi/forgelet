package workflow

import (
	"strings"
	"testing"
)

func TestActionMetadataRuntimesInputsAndOutputs(t *testing.T) {
	node := `name: Node fixture
description: lifecycle
inputs:
  value: {description: value, required: true, default: default}
outputs:
  answer: {description: answer}
runs:
  using: node20
  pre: pre.js
  pre-if: success()
  main: main.js
  post: post.js
  post-if: always()
`
	definition, err := ParseAction([]byte(node))
	if err != nil || definition.Using != "node20" || definition.Pre != "pre.js" || definition.Post != "post.js" || definition.Inputs["value"] != "default" {
		t.Fatal(definition, err)
	}
	composite := `name: Composite fixture
description: nested
inputs:
  value: {default: hello}
outputs:
  result: {value: '${{ steps.write.outputs.value }}'}
runs:
  using: composite
  steps:
    - id: write
      shell: sh
      run: echo 'value=${{ inputs.value }}' >> "$GITHUB_OUTPUT"
    - run: echo '${{ toJSON(inputs) }}'
      shell: sh
    - uses: ./nested
      with: {value: '${{ steps.write.outputs.value }}'}
`
	definition, err = ParseAction([]byte(composite))
	if err != nil || len(definition.Steps) != 3 || definition.Outputs["result"] != "${{ steps.write.outputs.value }}" {
		t.Fatal(definition, err)
	}
}

func TestActionMetadataRejectsUnsupportedAndUnsafeCapabilities(t *testing.T) {
	base := "name: Test\ndescription: Test\n"
	for name, source := range map[string]string{
		"malformed":                      "[",
		"empty":                          "",
		"oversized":                      strings.Repeat("x", (256<<10)+1),
		"multiple documents":             base + "---\n" + base,
		"unknown metadata":               base + "unexpected: true\n",
		"missing description":            "name: Test\nruns: {using: node24, main: main.js}\n",
		"inputs not mapping":             base + "inputs: []\n",
		"bad input name":                 base + "inputs: {'bad.key': {}}\n",
		"bad input definition":           base + "inputs: {good: {unknown: true}}\n",
		"bad input default":              base + "inputs: {good: {default: []}}\n",
		"unknown runs field":             base + "runs: {using: node24, unknown: true}\n",
		"missing runtime":                base + "runs: {}\n",
		"docker runtime":                 base + "runs: {using: docker, main: main.js}\n",
		"node cannot have steps":         base + "runs: {using: node24, main: main.js, steps: []}\n",
		"missing main":                   base + "runs: {using: node24}\n",
		"escape":                         base + "runs: {using: node24, main: ../outside.js}\n",
		"dynamic script":                 base + "runs: {using: node24, main: '${{ inputs.script }}'}\n",
		"pre condition":                  base + "runs: {using: node24, main: main.js, pre-if: 'unknown()'}\n",
		"composite post":                 base + "runs: {using: composite, post: post.js, steps: [{run: echo ok}]}\n",
		"composite no steps":             base + "runs: {using: composite, steps: []}\n",
		"duplicate step":                 base + "runs: {using: composite, steps: [{id: same, run: echo ok}, {id: same, run: echo ok}]}\n",
		"future step output":             base + "runs: {using: composite, steps: [{run: 'echo ${{ steps.future.outputs.value }}'}]}\n",
		"invalid output ID":              base + "runs: {using: node24, main: main.js}\noutputs: {'bad.key': {description: value}}\n",
		"unknown output field":           base + "runs: {using: node24, main: main.js}\noutputs: {good: {unknown: true}}\n",
		"JS output expression":           base + "runs: {using: node24, main: main.js}\noutputs: {good: {value: hello}}\n",
		"missing composite output value": base + "runs: {using: composite, steps: [{run: echo ok}]}\noutputs: {good: {description: hello}}\n",
		"unknown composite output":       base + "runs: {using: composite, steps: [{run: echo ok}]}\noutputs: {good: {value: '${{ steps.unknown.outputs.x }}'}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAction([]byte(source)); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
}
