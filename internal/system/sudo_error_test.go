package system

import (
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
)

func TestSudoError(t *testing.T) {
	tests := []struct {
		name         string
		err          SudoError
		wantCommands []string
		want         []string // parts of the message
	}{
		{
			name:         "missing on fedora",
			err:          SudoError{Status: SudoMissing, User: "phil", OS: config.Fedora},
			wantCommands: []string{"dnf install -y sudo", "usermod -aG wheel phil"},
			want: []string{
				"sudo isn't installed, and some steps need to run as root.",
				"run these commands as root, then log out and back in",
				"\n  dnf install -y sudo\n  usermod -aG wheel phil\n",
				"\n  su - -c 'dnf install -y sudo && usermod -aG wheel phil'",
			},
		},
		{
			name:         "missing on ubuntu",
			err:          SudoError{Status: SudoMissing, User: "phil", OS: config.Ubuntu},
			wantCommands: []string{"apt-get update", "apt-get install -y sudo", "usermod -aG sudo phil"},
			want:         []string{"su - -c 'apt-get update && apt-get install -y sudo && usermod -aG sudo phil'"},
		},
		{
			name:         "not allowed on fedora",
			err:          SudoError{Status: SudoNotAllowed, User: "phil", OS: config.Fedora, Message: "phil is not in the sudoers file."},
			wantCommands: []string{"usermod -aG wheel phil"},
			want: []string{
				"phil may not use sudo (phil is not in the sudoers file.), and some steps need to run as root.",
				"run this command as root",
				"su - -c 'usermod -aG wheel phil'",
			},
		},
		{
			name:         "not allowed on ubuntu",
			err:          SudoError{Status: SudoNotAllowed, User: "phil", OS: config.Ubuntu},
			wantCommands: []string{"usermod -aG sudo phil"},
			want:         []string{"phil may not use sudo, and", "su - -c 'usermod -aG sudo phil'"},
		},
		{
			name: "missing on another OS",
			err:  SudoError{Status: SudoMissing, User: "phil"},
			want: []string{"Install sudo as root, and add phil to the group that may use sudo (wheel or sudo). Then log out and back in"},
		},
		{
			name: "odd user name",
			err:  SudoError{Status: SudoNotAllowed, User: "o'neil", OS: config.Fedora},
			wantCommands: []string{
				`usermod -aG wheel 'o'\''neil'`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Commands(); !slices.Equal(got, tt.wantCommands) {
				t.Errorf("Commands() = %q, want %q", got, tt.wantCommands)
			}
			msg := tt.err.Error()
			for _, want := range tt.want {
				if !strings.Contains(msg, want) {
					t.Errorf("Error() =\n%s\nwant it to contain %q", msg, want)
				}
			}
		})
	}
}
