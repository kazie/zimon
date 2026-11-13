package cli

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"

	mcobra "github.com/muesli/mango-cobra"
	"github.com/muesli/roff"
	"github.com/spf13/cobra"
)

// newManCmd prints the man page, for packages to install (zimon man > zimon.1).
func newManCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "man",
		Short:  "Print the man page",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			page, err := mcobra.NewManPage(1, cmd.Root())
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), fixManPage(page.Build(roff.NewDocument()), manDate()))
			return err
		},
	}
}

var (
	// mango writes short flags with the long prefix: "--y --yes".
	shortFlagRe = regexp.MustCompile(`\\fB--([[:alnum:]]) --`)
	manDateRe   = regexp.MustCompile(`(?m)^(\.TH \S+ \d+ )"\d{4}-\d{2}-\d{2}"`)
)

// fixManPage corrects mango's short flags ("-y, --yes") and sets the date.
func fixManPage(page string, date time.Time) string {
	page = shortFlagRe.ReplaceAllString(page, `\fB-$1, --`)
	return manDateRe.ReplaceAllString(page, `${1}"`+date.Format(time.DateOnly)+`"`)
}

// manDate is $SOURCE_DATE_EPOCH, so package builds are reproducible, or today.
func manDate() time.Time {
	if s, err := strconv.ParseInt(os.Getenv("SOURCE_DATE_EPOCH"), 10, 64); err == nil {
		return time.Unix(s, 0).UTC()
	}
	return time.Now()
}
