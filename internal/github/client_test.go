package github

import "testing"

func TestNextPageURL(t *testing.T) {
	tests := []struct {
		name string
		link string
		want string
	}{
		{
			name: "with next",
			link: `<https://api.github.com/repos/foo/bar/forks?page=2&per_page=100>; rel="next", <https://api.github.com/repos/foo/bar/forks?page=5&per_page=100>; rel="last"`,
			want: "https://api.github.com/repos/foo/bar/forks?page=2&per_page=100",
		},
		{
			name: "no next",
			link: `<https://api.github.com/repos/foo/bar/forks?page=1&per_page=100>; rel="prev"`,
			want: "",
		},
		{
			name: "empty",
			link: "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextPageURL(tt.link)
			if got != tt.want {
				t.Errorf("nextPageURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
