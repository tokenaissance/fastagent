package channels

import (
	"reflect"
	"testing"
)

func TestSplitOutboundText(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "no marker — single bubble",
			in:   "hello world",
			want: []string{"hello world"},
		},
		{
			name: "single split",
			in:   "first\n" + SplitMessageMarker + "\nsecond",
			want: []string{"first", "second"},
		},
		{
			name: "marker without surrounding newlines still splits",
			in:   "first" + SplitMessageMarker + "second",
			want: []string{"first", "second"},
		},
		{
			name: "trailing marker drops empty tail",
			in:   "only one\n" + SplitMessageMarker + "\n",
			want: []string{"only one"},
		},
		{
			name: "consecutive markers don't produce blank bubbles",
			in:   "a\n" + SplitMessageMarker + "\n\n" + SplitMessageMarker + "\nb",
			want: []string{"a", "b"},
		},
		{
			name: "whitespace-only chunks dropped",
			in:   "   \n" + SplitMessageMarker + "\nactual content\n" + SplitMessageMarker + "\n\t",
			want: []string{"actual content"},
		},
		{
			name: "trims per-chunk whitespace",
			in:   "  first  \n" + SplitMessageMarker + "\n\tsecond\n",
			want: []string{"first", "second"},
		},
		{
			name: "empty input returns empty",
			in:   "",
			want: []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitOutboundText(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}
