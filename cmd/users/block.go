package users

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/flant/gitlaball/pkg/client"
	"github.com/flant/gitlaball/pkg/limiter"
	"github.com/flant/gitlaball/pkg/sort"
	"github.com/flant/gitlaball/pkg/util"

	"github.com/flant/gitlaball/cmd/common"

	"github.com/hashicorp/go-hclog"
	"github.com/spf13/cobra"
	"github.com/xanzy/go-gitlab"
)

var (
	blockBy, blockFieldValue string
	blockHosts               bool
)

func NewBlockCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "block [by]",
		Short: "Blocks an existing user",
		Long:  "Blocks an existing user. Only administrators can change attributes of a user.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			blockFieldValue = args[0]
			return Block()
		},
	}

	cmd.Flags().Var(util.NewEnumValue(&blockBy, "email", "username", "name"), "by", "Search user you want to block")
	cmd.MarkFlagRequired("by")

	cmd.Flags().BoolVar(&blockHosts, "hosts", false, "List hosts where user exists")

	return cmd
}

func Block() error {
	wg := common.Limiter
	data := make(chan interface{})

	fmt.Printf("Searching for user %q...\n", blockFieldValue)
	for _, h := range common.Client.Hosts {
		wg.Add(1)
		go listUsersSearch(h, blockBy, blockFieldValue, gitlab.ListUsersOptions{
			ListOptions: gitlab.ListOptions{
				PerPage: 100,
			},
		}, wg, data, common.Client.WithNoCache())
	}

	go func() {
		wg.Wait()
		close(data)
	}()

	toBlock := make(sort.Elements, 0)
	for e := range data {
		toBlock = append(toBlock, e)
	}

	if len(toBlock) == 0 {
		return fmt.Errorf("user not found: %s", blockFieldValue)
	}

	if blockHosts {
		for _, h := range toBlock.Hosts() {
			fmt.Println(h.Project)
		}
		return nil
	}

	util.AskUser(fmt.Sprintf("Do you really want to block user %q in %d gitlab(s) %v ?",
		blockFieldValue, len(toBlock.Hosts()), toBlock.Hosts().Projects()))

	blocked := make(chan interface{})
	for _, v := range toBlock.Typed() {
		wg.Add(1)
		go blockUser(v.Host, v.Struct.(*gitlab.User), wg, blocked)
	}

	go func() {
		wg.Wait()
		close(blocked)
	}()

	results := sort.FromChannel(blocked, &sort.Options{
		OrderBy:    []string{blockBy},
		StructType: gitlab.User{},
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', tabwriter.TabIndent)
	fmt.Fprintf(w, "COUNT\tUSER\tHOSTS\tCACHED\n")

	total := 0
	for _, v := range results {
		fmt.Fprintf(w, "[%d]\t%s\t%s\t[%s]\n", v.Count, v.Key, v.Elements.Hosts().Projects(), v.Cached)
		total++
	}

	fmt.Fprintf(w, "Blocked: %d\nErrors: %d\n", total, len(wg.Errors()))

	w.Flush()

	for _, err := range wg.Errors() {
		hclog.L().Error(err.Err.Error())
	}

	return nil

}

func blockUser(h *client.Host, user *gitlab.User, wg *limiter.Limiter, data chan<- interface{},
	options ...gitlab.RequestOptionFunc) {

	defer wg.Done()

	wg.Lock()
	err := h.Client.Users.BlockUser(user.ID, options...)
	if err != nil {
		wg.Error(h, err)
		wg.Unlock()
		return
	}
	wg.Unlock()

	data <- sort.Element{Host: h, Struct: user, Cached: false}
}