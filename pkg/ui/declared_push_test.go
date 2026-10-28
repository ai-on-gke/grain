package ui_test

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// A declared template's push set (grain/task-2536) is shown in the two
// places somebody looks for it: on a task filed from the template, and
// in its repository's declarations status.
func TestADeclaredPushSetIsShownOnItsTasksAndItsRepository(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	srv := ui.NewServerWithClient(client)
	target := declared
	templateID := model.RepoDeclaredID(model.DeclaredTemplate, declared, "release")
	push := model.RefPushSet{{Pattern: "refs/heads/candidate", Force: true}, {Pattern: "refs/tags/v*"}}
	if _, err := store.SyncRepoDeclarations(ctx, declared, "abc123", model.RepoDeclarations{
		Templates: []model.DeclaredTemplateFile{{
			Path:     model.TemplateDeclarationDir + "/release.yml",
			Template: model.Template{ID: templateID, Name: "release", Title: "Cut a release", Target: &target},
			Push:     push,
		}},
	}, time.Now().UTC(), time.UTC); err != nil {
		t.Fatalf("syncing declarations: %v", err)
	}

	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"templateId":"`+templateID+`"}`)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("filing from the template: %d %s", rec.Code, rec.Body)
	}
	filed := decode[ui.Task](t, rec)
	rec = do(t, srv, http.MethodGet, "/api/tasks/"+filed.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reading the task: %d %s", rec.Code, rec.Body)
	}
	detail := decode[ui.TaskDetail](t, rec)
	want := &ui.DeclaredPush{
		Repo: declared.String(), Path: model.TemplateDeclarationDir + "/release.yml", Commit: "abc123",
		Refs: push,
	}
	if !reflect.DeepEqual(detail.DeclaredPush, want) {
		t.Errorf("the task's declaredPush = %+v, want %+v", detail.DeclaredPush, want)
	}

	rec = do(t, srv, http.MethodPut, "/api/repos/acme/widgets/declarations", `{"declarations":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200: %s", rec.Code, rec.Body)
	}
	status := decode[ui.RepoDefaults](t, rec).DeclarationsStatus
	if status == nil || len(status.Pushes) != 1 || !reflect.DeepEqual(status.Pushes[0], *want) {
		t.Errorf("the repository's declarations status pushes = %+v, want [%+v]", status, *want)
	}
}
