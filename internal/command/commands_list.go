// Port of tart's Commands/List.swift.
//go:build darwin

package command

import (
	"context"
	"fmt"
	"sort"
	"time"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
	"github.com/deploymenttheory/weave/internal/vmstorage"
)

type ListVMInfo struct {
	Source   string
	Name     string
	Disk     int
	Size     int
	Accessed string
	Running  bool
	State    string
}

// ListCommand ports the List command.
type ListCommand struct {
	Source string // "", "local" or "oci"
	Format Format
	Quiet  bool
}

func (c *ListCommand) Validate() error {
	if c.Source != "" && c.Source != "local" && c.Source != "oci" {
		return weaveerrors.ErrGeneric("'%s' is not a valid <source>", c.Source)
	}
	return nil
}

func (c *ListCommand) Run(ctx context.Context) error {
	var infos []ListVMInfo

	if c.Source == "" || c.Source == "local" {
		localStorage, err := vmstorage.NewVMStorageLocal()
		if err != nil {
			return err
		}
		entries, err := localStorage.List()
		if err != nil {
			return err
		}
		batch := make([]ListVMInfo, 0, len(entries))
		for _, entry := range entries {
			info, err := c.VMInfo("local", entry.Name, entry.VMDir)
			if err != nil {
				return err
			}
			batch = append(batch, info)
		}
		infos = append(infos, SortedInfos(batch)...)
	}

	if c.Source == "" || c.Source == "oci" {
		ociStorage, err := vmstorage.NewVMStorageOCI()
		if err != nil {
			return err
		}
		entries, err := ociStorage.List()
		if err != nil {
			return err
		}
		batch := make([]ListVMInfo, 0, len(entries))
		for _, entry := range entries {
			info, err := c.VMInfo("OCI", entry.Name, entry.VMDir)
			if err != nil {
				return err
			}
			batch = append(batch, info)
		}
		infos = append(infos, SortedInfos(batch)...)
	}

	if c.Quiet {
		for _, info := range infos {
			fmt.Println(info.Name)
		}
	} else {
		anyInfos := make([]any, 0, len(infos))
		for _, info := range infos {
			anyInfos = append(anyInfos, info)
		}
		fmt.Println(c.Format.RenderList(anyInfos))
	}
	return nil
}

func (c *ListCommand) VMInfo(source string, name string, vmDir *vmdirectory.VMDirectory) (ListVMInfo, error) {
	diskGB, err := vmDir.SizeGB()
	if err != nil {
		return ListVMInfo{}, err
	}
	sizeGB, err := vmDir.AllocatedSizeGB()
	if err != nil {
		return ListVMInfo{}, err
	}
	accessDate, err := vmDir.AccessDate()
	if err != nil {
		return ListVMInfo{}, err
	}
	running, err := vmDir.Running()
	if err != nil {
		return ListVMInfo{}, err
	}
	state, err := vmDir.State()
	if err != nil {
		return ListVMInfo{}, err
	}

	return ListVMInfo{
		Source:   source,
		Name:     name,
		Disk:     diskGB,
		Size:     sizeGB,
		Accessed: c.formatAccessDate(accessDate),
		Running:  running,
		State:    string(state),
	}, nil
}

func SortedInfos(infos []ListVMInfo) []ListVMInfo {
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// formatAccessDate mirrors List.formatAccessDate: relative wording for text
// output, ISO 8601 for JSON.
func (c *ListCommand) formatAccessDate(accessDate time.Time) string {
	if c.Format == FormatJSON {
		return accessDate.UTC().Format(time.RFC3339)
	}
	return relativeDateString(accessDate)
}

// relativeDateString approximates RelativeDateTimeFormatter's full style.
func relativeDateString(date time.Time) string {
	elapsed := time.Since(date)
	if elapsed < 0 {
		return "in the future"
	}

	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%d seconds ago", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(elapsed.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(elapsed.Hours()/24))
	}
}
