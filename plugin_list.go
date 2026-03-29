package main

import "fmt"

func cmdList() {
	discovered := discoverPlugins()
	disabled := loadDisabledPlugins()

	if len(discovered) == 0 {
		fmt.Println("No plugins found.")
		return
	}

	// Calculate column widths
	idW, nameW, verW := 2, 4, 7
	for _, dp := range discovered {
		if len(dp.Manifest.ID) > idW {
			idW = len(dp.Manifest.ID)
		}
		if len(dp.Manifest.Name) > nameW {
			nameW = len(dp.Manifest.Name)
		}
		if len(dp.Manifest.Version) > verW {
			verW = len(dp.Manifest.Version)
		}
	}

	fmt.Printf("%-*s  %-*s  %-*s  %-8s  %s\n", idW, "ID", nameW, "NAME", verW, "VERSION", "SOURCE", "STATUS")
	for _, dp := range discovered {
		status := "static"
		if disabled[dp.Manifest.ID] {
			status = "disabled"
		} else if dp.Manifest.Run != "" {
			status = "managed"
		}
		fmt.Printf("%-*s  %-*s  %-*s  %-8s  %s\n",
			idW, dp.Manifest.ID,
			nameW, dp.Manifest.Name,
			verW, dp.Manifest.Version,
			string(dp.Source), status,
		)
	}
}
