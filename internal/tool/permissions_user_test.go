package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestRequiresConfirmation_RelativePath(t *testing.T) {
	cwd, _ := os.Getwd()
	fmt.Printf("Test CWD: %s\n", cwd)

	// Test write_file with relative path
	wt := WriteFileTool{}
	args := json.RawMessage(`{"path":"src/lib/pages/Chat.svelte","content":"test"}`)
	result := wt.RequiresConfirmation(args)
	fmt.Printf("WriteFileTool.RequiresConfirmation(relative) = %v (want false)\n", result)
	if result {
		t.Errorf("WriteFileTool.RequiresConfirmation(relative path) = true, want false")
	}

	// Test target_edit with relative path
	te := NewTargetEditTool()
	args2 := json.RawMessage(`{"file":"src/lib/pages/Chat.svelte","search":"old","replace":"new"}`)
	result2 := te.RequiresConfirmation(args2)
	fmt.Printf("TargetEditTool.RequiresConfirmation(relative) = %v (want false)\n", result2)
	if result2 {
		t.Errorf("TargetEditTool.RequiresConfirmation(relative path) = true, want false")
	}

	// Test with absolute path in CWD
	absPath := cwd + "/src/lib/pages/Chat.svelte"
	args3 := json.RawMessage(fmt.Sprintf(`{"file":"%s","search":"old","replace":"new"}`, absPath))
	result3 := te.RequiresConfirmation(args3)
	fmt.Printf("TargetEditTool.RequiresConfirmation(abs in cwd) = %v (want false)\n", result3)

	// Test what getToolParam actually returns
	fmt.Printf("getToolParam(args2, 'file') = %q\n", getToolParam(args2, "file"))
	fmt.Printf("getToolParam(args, 'path') = %q\n", getToolParam(args, "path"))

	// Test IsSafePath directly
	fmt.Printf("IsSafePath('src/lib/pages/Chat.svelte') = %v\n", IsSafePath("src/lib/pages/Chat.svelte"))
	fmt.Printf("IsSafePath('./src/lib/pages/Chat.svelte') = %v\n", IsSafePath("./src/lib/pages/Chat.svelte"))

	// Test what the middleware would see
	// The middleware calls reg.Get(tc.Function.Name) - if the tool is NOT in the registry,
	// it falls through to confirmation. Let's verify the registry lookup.
	fmt.Println("\n--- Registry test ---")
	reg := NewRegistry()
	reg.Register(wt)
	reg.Register(te)

	if tool := reg.Get("write_file"); tool != nil {
		fmt.Printf("Registry found write_file: RequiresConfirmation = %v\n", tool.RequiresConfirmation(args))
	} else {
		fmt.Println("Registry: write_file NOT FOUND")
	}

	if tool := reg.Get("target_edit"); tool != nil {
		fmt.Printf("Registry found target_edit: RequiresConfirmation = %v\n", tool.RequiresConfirmation(args2))
	} else {
		fmt.Println("Registry: target_edit NOT FOUND")
	}

	// Now test with an EMPTY registry (simulating root planning registry)
	fmt.Println("\n--- Empty registry (simulates root planning agent) ---")
	emptyReg := NewRegistry()
	if tool := emptyReg.Get("target_edit"); tool != nil {
		fmt.Println("Empty registry found target_edit (unexpected!)")
	} else {
		fmt.Println("Empty registry: target_edit NOT FOUND -> middleware would fall through to confirmation!")
	}
}
