package cli

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/kazie/zimon/internal/server"
	"github.com/kazie/zimon/internal/store"
	"github.com/kazie/zimon/internal/ui"
)

var tokenNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func newTokenCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage tokens for zimon serve's API and admin page",
		Long: `Tokens let you manage the library through zimon serve: from the admin page
in a browser, or from another computer. Reading books never needs one.

Each token has a name (the device or person it is for), so you can remove one
without the others. A token is shown once, when it is made; zimon keeps only a
hash of it.`,
		Example: `  zimon token add laptop
  zimon token list
  zimon token remove laptop`,
	}
	add := &cobra.Command{
		Use:   "add NAME",
		Short: "Make a token and show it once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !tokenNameRe.MatchString(name) {
				return fmt.Errorf("token name %q: use letters, digits, dots, dashes and underscores", name)
			}
			st, err := a.store()
			if err != nil {
				return err
			}
			token, hash := server.NewToken()
			if err := st.AddToken(cmd.Context(), name, hash); errors.Is(err, store.ErrTokenExists) {
				return fmt.Errorf("there is already a token called %s (zimon token remove %s makes room)", name, name)
			} else if err != nil {
				return err
			}
			a.out.Println(token)
			a.out.Success("Token %s made. It is shown only now: keep it somewhere safe.", ui.Bold.Render(name))
			a.out.Hint("Use it in the admin page of zimon serve, or as: Authorization: Bearer <token>")
			return nil
		},
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List the tokens",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.store()
			if err != nil {
				return err
			}
			toks, err := st.Tokens(cmd.Context())
			if err != nil {
				return err
			}
			if len(toks) == 0 {
				a.out.Hint("No tokens yet. Make one with: zimon token add NAME")
				return nil
			}
			t := ui.NewTable("NAME", "MADE", "LAST USED")
			for _, tok := range toks {
				used := "never"
				if !tok.LastUsed.IsZero() {
					used = when(tok.LastUsed)
				}
				t.Add(ui.Bold.Render(tok.Name), tok.Created.Local().Format("2006-01-02"), used)
			}
			t.Render(a.out.Out, a.out.Width())
			return nil
		},
	}
	remove := &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove a token; whoever has it can no longer use it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := a.store()
			if err != nil {
				return err
			}
			ok, err := st.RemoveToken(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no token called %s (see: zimon token list)", args[0])
			}
			a.out.Success("Removed token %s", ui.Bold.Render(args[0]))
			return nil
		},
	}
	cmd.AddCommand(add, list, remove)
	return cmd
}
