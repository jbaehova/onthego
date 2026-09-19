package state

import "testing"

func TestRelationUsesSnapshotAncestry(t *testing.T) {
	project := Default("project")
	project.Snapshots["root"] = Snapshot{ID: "root"}
	project.Snapshots["local"] = Snapshot{ID: "local", Parents: []string{"root"}}
	project.Snapshots["remote"] = Snapshot{ID: "remote", Parents: []string{"root"}}
	project.Environments["local"] = Environment{ID: "local", LatestSnapshotID: "root"}
	project.Environments["daytona"] = Environment{ID: "daytona", LatestSnapshotID: "remote"}
	if got := Relation(project, "local", "daytona"); got != "remote_ahead" {
		t.Fatalf("got %s", got)
	}
	project.Environments["local"] = Environment{ID: "local", LatestSnapshotID: "local"}
	if got := Relation(project, "local", "daytona"); got != "diverged" {
		t.Fatalf("got %s", got)
	}
}
