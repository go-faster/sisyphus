package sheets

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToStrings(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   [][]any
		want [][]string
	}{
		{name: "empty", in: nil, want: [][]string{}},
		{
			name: "renders scalars",
			in:   [][]any{{"a", 1.5, true}},
			want: [][]string{{"a", "1.5", "true"}},
		},
		{
			// The API drops trailing empty cells, so rows arrive ragged.
			name: "pads short rows to the widest",
			in:   [][]any{{"a", "b", "c"}, {"d"}, {}},
			want: [][]string{{"a", "b", "c"}, {"d", "", ""}, {"", "", ""}},
		},
		{
			name: "nil cell becomes empty string",
			in:   [][]any{{"a", nil, "c"}},
			want: [][]string{{"a", "", "c"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, toStrings(tt.in))
		})
	}
}

func TestToValues(t *testing.T) {
	require.Equal(t, [][]any{{"a", ""}, {"b", "c"}}, toValues([][]string{{"a", ""}, {"b", "c"}}))
	require.Equal(t, [][]any{}, toValues(nil))
}

func TestInputOption(t *testing.T) {
	require.Equal(t, "USER_ENTERED", inputOption(false))
	require.Equal(t, "RAW", inputOption(true))
}

func TestReadOnlyRefusesWrites(t *testing.T) {
	c := &Client{readOnly: true}
	ctx := t.Context()

	_, err := c.Write(ctx, testID, "A1", [][]string{{"x"}}, false)
	require.ErrorContains(t, err, "read-only")

	_, err = c.Append(ctx, testID, "A1", [][]string{{"x"}}, false)
	require.ErrorContains(t, err, "read-only")

	_, err = c.Clear(ctx, testID, "A1")
	require.ErrorContains(t, err, "read-only")
}

func TestWriteAndClearNeedARange(t *testing.T) {
	c := &Client{}
	ctx := t.Context()

	_, err := c.Write(ctx, testID, "", [][]string{{"x"}}, false)
	require.ErrorContains(t, err, "explicit range")

	_, err = c.Clear(ctx, testID, "")
	require.ErrorContains(t, err, "explicit range")
}
