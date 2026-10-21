package castview

import "testing"

func TestDeriveLockView(t *testing.T) {
	cases := []struct {
		name                             string
		visible, teach, guard            bool
		mode                             string
		wantVis, wantFS, wantIn, wantBar bool
	}{
		{name: "idle", mode: "window"},
		{
			name:    "window broadcast",
			visible: true, mode: "window",
			wantVis: true, wantBar: true,
		},
		{
			name:    "fullscreen broadcast locks input but keeps the bar",
			visible: true, mode: "fullscreen",
			wantVis: true, wantFS: true, wantIn: true, wantBar: true,
		},
		{
			name:    "teacher lock covers a window broadcast and hides the bar",
			visible: true, teach: true, mode: "window",
			wantVis: true, wantFS: true, wantIn: true,
		},
		{
			name:  "guard lock alone is a fullscreen input lock",
			guard: true, mode: "window",
			wantVis: true, wantFS: true, wantIn: true,
		},
		{
			name:    "guard lock stays after the teacher unlocks",
			visible: true, guard: true, mode: "window",
			wantVis: true, wantFS: true, wantIn: true,
		},
		{
			name:    "unlocking both returns a window broadcast to a movable window",
			visible: true, mode: "window",
			wantVis: true, wantBar: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveLockView(tc.visible, tc.teach, tc.guard, tc.mode)
			if got.visible != tc.wantVis || got.fullscreen != tc.wantFS || got.inputLock != tc.wantIn || got.bar != tc.wantBar {
				t.Fatalf("deriveLockView() = %+v, want visible=%v fullscreen=%v input=%v bar=%v",
					got, tc.wantVis, tc.wantFS, tc.wantIn, tc.wantBar)
			}
		})
	}
}
