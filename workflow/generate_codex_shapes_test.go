package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestGenerateCodexPreservesTaskModelAndEffortChoices(t *testing.T) {
	opts := plannerFixture(t, goodPlannerResponse)
	if err := Generate(context.Background(), []string{"*.feature"}, opts); err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{"a": {"small", "low"}, "b": {"large", "xhigh"}, "review-all": {"large", "high"}}
	for _, task := range plan.Tasks {
		if got := [2]string{task.Model, task.ReasoningEffort}; got != want[task.ID] {
			t.Errorf("task %s model/effort = %v, want %v", task.ID, got, want[task.ID])
		}
		delete(want, task.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing tasks: %v", want)
	}
}

func TestGenerateCodexPreservesGroupedAndSerialShapes(t *testing.T) {
	for _, shape := range []string{"grouped", "serial"} {
		t.Run(shape, func(t *testing.T) {
			var response codexGenerationPlan
			if err := json.Unmarshal([]byte(goodPlannerResponse), &response); err != nil {
				t.Fatal(err)
			}
			if shape == "grouped" {
				response.Tasks = response.Tasks[:1]
				response.Tasks[0].Features = []string{"a.feature", "b.feature"}
				response.Tasks[0].Rationale = "Both features modify the shared service and acceptance fixtures; implement them together."
			} else {
				response.Tasks[1].Needs = []string{"a"}
				response.Tasks[1].Rationale = "Feature b extends the shared service and test fixtures created by a; wait for a before modifying those files."
			}
			content, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			opts := plannerFixture(t, string(content))
			if err := Generate(context.Background(), []string{"*.feature"}, opts); err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(opts.Output)
			if err != nil {
				t.Fatal(err)
			}
			if shape == "grouped" {
				if got := taskIDs(plan.Tasks); !reflect.DeepEqual(got, []string{"a", "review-all"}) {
					t.Fatalf("task IDs = %v", got)
				}
				if got := plan.Tasks[0].Features; !reflect.DeepEqual(got, []string{"a.feature", "b.feature"}) {
					t.Fatalf("grouped features = %v", got)
				}
				if len(plan.Tasks[0].Scenarios) != 2 {
					t.Fatalf("grouped scenarios = %d", len(plan.Tasks[0].Scenarios))
				}
				if got := plan.Tasks[1].Needs; !reflect.DeepEqual(got, []string{"a"}) {
					t.Fatalf("review dependencies = %v", got)
				}
			} else {
				if got := taskIDs(plan.Tasks); !reflect.DeepEqual(got, []string{"a", "b", "review-all"}) {
					t.Fatalf("task IDs = %v", got)
				}
				if got := plan.Tasks[1].Needs; !reflect.DeepEqual(got, []string{"a"}) {
					t.Fatalf("shared feature dependency = %v", got)
				}
				if got := plan.Tasks[2].Needs; !reflect.DeepEqual(got, []string{"a", "b"}) {
					t.Fatalf("review dependencies = %v", got)
				}
			}
		})
	}
}
