package main

import "fmt"

func cmdList() {
	discovered := discoverPlugins()
	states, online := fetchPluginStates()

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
		// Install kind is a disk fact (a `run` command → managed process).
		status := "static"
		if dp.Manifest.Run != "" {
			status = "managed"
		}
		// Disabled is runtime state owned by the actuator; overlay it only
		// when the actuator answered.
		if online {
			if st, ok := states[dp.Manifest.ID]; ok && !st.Enabled {
				status = "disabled"
			}
		}
		fmt.Printf("%-*s  %-*s  %-*s  %-8s  %s\n",
			idW, dp.Manifest.ID,
			nameW, dp.Manifest.Name,
			verW, dp.Manifest.Version,
			string(dp.Source), status,
		)
	}

	// Be honest when we couldn't determine disabled state rather than
	// silently showing everything as enabled.
	if !online {
		fmt.Println("\nNote: BranchKit isn't running — disabled state unavailable (STATUS shows install kind only).")
	}
}
