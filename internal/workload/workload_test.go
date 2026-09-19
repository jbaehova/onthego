package workload

import "testing"

func TestAgentWorkloadMayUseCPU(t *testing.T) {
	definition := Definition{SchemaVersion: SchemaVersion, Name: "agent-sync", Kind: "agent", Image: "ubuntu:24.04", Argv: []string{"/bin/true"}, Workdir: "/workspace", TimeoutSeconds: 60}
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGPUKindsRequireGPU(t *testing.T) {
	definition := Definition{SchemaVersion: SchemaVersion, Name: "train", Kind: "fine_tune", Image: "image", Argv: []string{"python"}, Workdir: "/workspace", TimeoutSeconds: 60}
	if err := definition.Validate(); err == nil {
		t.Fatal("expected fine_tune without a GPU to fail")
	}
}
