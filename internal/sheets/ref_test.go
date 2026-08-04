package sheets

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const testID = "1NWzdPRBdyxDQIf4P_pfh5WbHeYReT-u6bpDHvwffajI"

func TestParseID(t *testing.T) {
	for _, tt := range []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "bare id", in: testID, want: testID},
		{name: "trims space", in: "  " + testID + "\n", want: testID},
		{
			name: "edit url",
			in:   "https://docs.google.com/spreadsheets/d/" + testID + "/edit",
			want: testID,
		},
		{
			name: "url with gid and range",
			in:   "https://docs.google.com/spreadsheets/d/" + testID + "/edit?gid=0#gid=0&range=B4",
			want: testID,
		},
		{
			name: "url with user prefix",
			in:   "https://docs.google.com/u/1/spreadsheets/d/" + testID + "/edit",
			want: testID,
		},
		{name: "empty", in: "", wantErr: true},
		{name: "blank", in: "   ", wantErr: true},
		{name: "unrelated url", in: "https://example.com/foo", wantErr: true},
		{name: "id with slash", in: "abc/def", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseID(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestClientResolve(t *testing.T) {
	const other = "2ZZZzzz_other-Sheet"

	t.Run("uses default when omitted", func(t *testing.T) {
		c := &Client{def: testID}
		got, err := c.resolve("")
		require.NoError(t, err)
		require.Equal(t, testID, got)
	})

	t.Run("no default is an error", func(t *testing.T) {
		c := &Client{}
		_, err := c.resolve("  ")
		require.Error(t, err)
	})

	t.Run("accepts a url", func(t *testing.T) {
		c := &Client{}
		got, err := c.resolve("https://docs.google.com/spreadsheets/d/" + testID + "/edit#gid=0")
		require.NoError(t, err)
		require.Equal(t, testID, got)
	})

	t.Run("allowlist rejects others", func(t *testing.T) {
		c := &Client{allow: map[string]bool{testID: true}}
		got, err := c.resolve(testID)
		require.NoError(t, err)
		require.Equal(t, testID, got)

		_, err = c.resolve(other)
		require.ErrorContains(t, err, "not in the allowlist")
	})

	t.Run("empty allowlist permits any", func(t *testing.T) {
		c := &Client{}
		got, err := c.resolve(other)
		require.NoError(t, err)
		require.Equal(t, other, got)
	})

	// The default is resolved before the allowlist is consulted, so a default
	// outside the list still works — New rejects that combination up front.
	t.Run("default bypasses allowlist at call time", func(t *testing.T) {
		c := &Client{def: testID, allow: map[string]bool{other: true}}
		got, err := c.resolve("")
		require.NoError(t, err)
		require.Equal(t, testID, got)
	})
}

func TestNewRejectsDefaultOutsideAllowlist(t *testing.T) {
	_, err := New(t.Context(), Options{
		Default: testID,
		Allow:   []string{"2ZZZzzz_other-Sheet"},
	})
	require.ErrorContains(t, err, "not in the allowlist")
}

func TestNewRejectsBadReferences(t *testing.T) {
	_, err := New(t.Context(), Options{Allow: []string{"https://example.com/nope"}})
	require.ErrorContains(t, err, "allow")

	_, err = New(t.Context(), Options{Default: "https://example.com/nope"})
	require.ErrorContains(t, err, "default spreadsheet")
}
