package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// GockerGUI represents the main GUI application
type GockerGUI struct {
	app            fyne.App
	window         fyne.Window
	containerList  *widget.List
	containers     []ContainerState
	selectedIndex  int
	logViewer      *widget.Entry
	commandEntry   *widget.Entry
	cpuLimitEntry  *widget.Entry
	memoryLimitEntry *widget.Entry
	volumeEntry    *widget.Entry
	detachedCheck  *widget.Check
	detailsText    *widget.RichText
}

// NewGockerGUI creates a new GUI instance
func NewGockerGUI() *GockerGUI {
	myApp := app.NewWithID("com.gocker.gui")

	window := myApp.NewWindow("Gocker - Container Management")
	window.Resize(fyne.NewSize(1000, 700))
	window.CenterOnScreen()

	return &GockerGUI{
		app:          myApp,
		window:       window,
		selectedIndex: -1,
	}
}

// Run starts the GUI application
func (gui *GockerGUI) Run() {
	gui.setupUI()
	gui.refreshContainers()
	
	// Auto-refresh container list every 2 seconds
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			gui.refreshContainers()
		}
	}()

	gui.window.ShowAndRun()
}

// setupUI creates the main UI layout
func (gui *GockerGUI) setupUI() {
	// Left panel: Container list and actions
	leftPanel := gui.createLeftPanel()
	
	// Right panel: Container details and logs
	rightPanel := gui.createRightPanel()
	
	// Bottom panel: Container creation form
	bottomPanel := gui.createBottomPanel()
	
	// Main layout - split view
	mainSplit := container.NewHSplit(leftPanel, rightPanel)
	mainSplit.SetOffset(0.5) // 50/50 split
	
	// Top to bottom layout
	content := container.NewBorder(
		widget.NewLabelWithStyle("Gocker Container Management", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		bottomPanel,
		nil,
		nil,
		mainSplit,
	)
	
	gui.window.SetContent(content)
}

// createLeftPanel creates the container list panel
func (gui *GockerGUI) createLeftPanel() fyne.CanvasObject {
	// Container list
	gui.containerList = widget.NewList(
		func() int {
			return len(gui.containers)
		},
		func() fyne.CanvasObject {
			return container.NewHBox(
				widget.NewLabel("Container"),
				widget.NewLabel("Status"),
			)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id >= len(gui.containers) {
				return
			}
			cont := gui.containers[id]
			box := obj.(*fyne.Container)
			labels := box.Objects
			
			// Container ID (first 12 chars)
			containerID := cont.ID
			if len(containerID) > 12 {
				containerID = containerID[:12]
			}
			labels[0].(*widget.Label).SetText(containerID)
			
			// Update status
			statusLabel := labels[1].(*widget.Label)
			statusLabel.SetText(cont.Status)
			switch cont.Status {
			case "running":
				statusLabel.Importance = widget.HighImportance
			case "stopped", "exited":
				statusLabel.Importance = widget.MediumImportance
			default:
				statusLabel.Importance = widget.LowImportance
			}
		},
	)
	
	gui.containerList.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(gui.containers) {
			gui.selectedIndex = int(id)
			gui.showContainerDetails(gui.containers[id])
		}
	}
	
	// Action buttons
	stopBtn := widget.NewButton("Stop", func() {
		gui.stopSelectedContainer()
	})
	
	removeBtn := widget.NewButton("Remove", func() {
		gui.removeSelectedContainer()
	})
	
	refreshBtn := widget.NewButton("Refresh", func() {
		gui.refreshContainers()
	})
	
	buttons := container.NewHBox(stopBtn, removeBtn, refreshBtn)
	
	listContainer := container.NewBorder(
		widget.NewLabel("Containers:"),
		buttons,
		nil,
		nil,
		gui.containerList,
	)
	
	// Container details panel
	detailsLabel := widget.NewLabel("Container Details")
	detailsLabel.TextStyle = fyne.TextStyle{Bold: true}
	
	gui.detailsText = widget.NewRichTextFromMarkdown("Select a container to view details")
	detailsScroll := container.NewScroll(gui.detailsText)
	detailsScroll.SetMinSize(fyne.NewSize(300, 200))
	
	detailsPanel := container.NewBorder(
		detailsLabel,
		nil,
		nil,
		nil,
		detailsScroll,
	)
	
	split := container.NewHSplit(listContainer, detailsPanel)
	split.SetOffset(0.6) // 60% for list, 40% for details
	
	return split
}

// createRightPanel creates the log viewer panel
func (gui *GockerGUI) createRightPanel() *fyne.Container {
	logLabel := widget.NewLabel("Container Logs")
	logLabel.TextStyle = fyne.TextStyle{Bold: true}
	
	gui.logViewer = widget.NewMultiLineEntry()
	gui.logViewer.Disable()
	gui.logViewer.Wrapping = fyne.TextWrapWord
	
	logScroll := container.NewScroll(gui.logViewer)
	logScroll.SetMinSize(fyne.NewSize(400, 400))
	
	clearLogBtn := widget.NewButton("Clear", func() {
		gui.logViewer.SetText("")
	})
	
	return container.NewBorder(
		logLabel,
		clearLogBtn,
		nil,
		nil,
		logScroll,
	)
}

// createBottomPanel creates the container creation form
func (gui *GockerGUI) createBottomPanel() *fyne.Container {
	formLabel := widget.NewLabel("Create New Container")
	formLabel.TextStyle = fyne.TextStyle{Bold: true}
	
	// Command input
	commandLabel := widget.NewLabel("Command:")
	gui.commandEntry = widget.NewEntry()
	gui.commandEntry.SetPlaceHolder("e.g., /bin/busybox sh -c 'while true; do echo Hello; sleep 5; done'")
	
	// Resource limits
	cpuLabel := widget.NewLabel("CPU Limit:")
	gui.cpuLimitEntry = widget.NewEntry()
	gui.cpuLimitEntry.SetPlaceHolder("e.g., 1 or 0.5 or max")
	
	memoryLabel := widget.NewLabel("Memory Limit:")
	gui.memoryLimitEntry = widget.NewEntry()
	gui.memoryLimitEntry.SetPlaceHolder("e.g., 512M, 1G, or max")
	
	// Volume mount
	volumeLabel := widget.NewLabel("Volume Mount:")
	gui.volumeEntry = widget.NewEntry()
	gui.volumeEntry.SetPlaceHolder("e.g., /host/path:/container/path")
	
	// Detached mode
	gui.detachedCheck = widget.NewCheck("Run in background (detached)", nil)
	
	// Create button
	createBtn := widget.NewButton("Create Container", func() {
		gui.createContainer()
	})
	createBtn.Importance = widget.HighImportance
	
	// Form layout
	form := container.NewGridWithColumns(2,
		commandLabel, gui.commandEntry,
		cpuLabel, gui.cpuLimitEntry,
		memoryLabel, gui.memoryLimitEntry,
		volumeLabel, gui.volumeEntry,
		widget.NewLabel(""), gui.detachedCheck,
	)
	
	return container.NewBorder(
		formLabel,
		createBtn,
		nil,
		nil,
		form,
	)
}

// refreshContainers refreshes the container list
func (gui *GockerGUI) refreshContainers() {
	containers, err := gui.loadAllContainers()
	if err != nil {
		dialog.ShowError(err, gui.window)
		return
	}
	
	gui.containers = containers
	gui.containerList.Refresh()
}

// loadAllContainers loads all containers from state directory
func (gui *GockerGUI) loadAllContainers() ([]ContainerState, error) {
	if err := ensureStateDir(); err != nil {
		return nil, err
	}
	
	files, err := os.ReadDir(containersDir)
	if err != nil {
		return nil, err
	}
	
	var containers []ContainerState
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		
		containerID := strings.TrimSuffix(file.Name(), ".json")
		state, err := loadContainerState(containerID)
		if err != nil {
			continue
		}
		
		// Check if process is still running
		if state.Status == "running" {
			if err := syscall.Kill(state.PID, 0); err != nil {
				state.Status = "exited"
				updateContainerStatus(containerID, "exited")
			}
		}
		
		containers = append(containers, *state)
	}
	
	return containers, nil
}

// showContainerDetails displays container details
func (gui *GockerGUI) showContainerDetails(container ContainerState) {
	details := fmt.Sprintf(`# Container Details

**ID:** %s
**Status:** %s
**PID:** %d
**Created:** %s
**Command:** %s
**Detached:** %v
**Veth Host:** %s
`, 
		container.ID,
		container.Status,
		container.PID,
		container.CreatedAt.Format("2006-01-02 15:04:05"),
		strings.Join(container.Command, " "),
		container.Detached,
		container.VethHost,
	)
	
	// Update details panel
	gui.detailsText.ParseMarkdown(details)
	
	// Update log viewer
	if container.LogFile != "" {
		logContent, err := os.ReadFile(container.LogFile)
		if err == nil {
			gui.logViewer.SetText(string(logContent))
		} else {
			gui.logViewer.SetText(fmt.Sprintf("Error reading log file: %v", err))
		}
	} else {
		gui.logViewer.SetText("No logs available")
	}
}

// createContainer creates a new container
func (gui *GockerGUI) createContainer() {
	command := strings.TrimSpace(gui.commandEntry.Text)
	if command == "" {
		dialog.ShowError(fmt.Errorf("command is required"), gui.window)
		return
	}
	
	// Build gocker command
	args := []string{"run"}
	
	if cpuLimit := strings.TrimSpace(gui.cpuLimitEntry.Text); cpuLimit != "" {
		args = append(args, "--cpu-limit", cpuLimit)
	}
	
	if memoryLimit := strings.TrimSpace(gui.memoryLimitEntry.Text); memoryLimit != "" {
		args = append(args, "--memory-limit", memoryLimit)
	}
	
	if volume := strings.TrimSpace(gui.volumeEntry.Text); volume != "" {
		args = append(args, "--volume", volume)
	}
	
	if gui.detachedCheck.Checked {
		args = append(args, "--detach")
	}
	
	// Split command into parts
	commandParts := strings.Fields(command)
	args = append(args, commandParts...)
	
	// Execute gocker command
	cmd := exec.Command("/proc/self/exe", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	if err := cmd.Start(); err != nil {
		dialog.ShowError(fmt.Errorf("failed to start container: %v", err), gui.window)
		return
	}
	
	// Show success message
	dialog.ShowInformation("Container Started", 
		fmt.Sprintf("Container started successfully!\nCommand: %s", command), 
		gui.window)
	
	// Clear form
	gui.commandEntry.SetText("")
	gui.cpuLimitEntry.SetText("")
	gui.memoryLimitEntry.SetText("")
	gui.volumeEntry.SetText("")
	gui.detachedCheck.SetChecked(false)
	
	// Refresh container list
	time.Sleep(500 * time.Millisecond) // Give it time to create state file
	gui.refreshContainers()
}

// stopSelectedContainer stops the selected container
func (gui *GockerGUI) stopSelectedContainer() {
	if gui.selectedIndex < 0 || gui.selectedIndex >= len(gui.containers) {
		dialog.ShowError(fmt.Errorf("please select a container"), gui.window)
		return
	}
	
	container := gui.containers[gui.selectedIndex]
	if container.Status != "running" {
		dialog.ShowError(fmt.Errorf("container is not running"), gui.window)
		return
	}
	
	// Confirm action
	dialog.ShowConfirm("Stop Container", 
		fmt.Sprintf("Are you sure you want to stop container %s?", container.ID[:12]),
		func(confirmed bool) {
			if confirmed {
				cmd := exec.Command("/proc/self/exe", "stop", container.ID)
				output, err := cmd.CombinedOutput()
				if err != nil {
					dialog.ShowError(fmt.Errorf("failed to stop container: %v\n%s", err, output), gui.window)
					return
				}
				
				dialog.ShowInformation("Container Stopped", 
					fmt.Sprintf("Container %s stopped successfully", container.ID[:12]), 
					gui.window)
				
				gui.refreshContainers()
			}
		}, gui.window)
}

// removeSelectedContainer removes the selected container
func (gui *GockerGUI) removeSelectedContainer() {
	if gui.selectedIndex < 0 || gui.selectedIndex >= len(gui.containers) {
		dialog.ShowError(fmt.Errorf("please select a container"), gui.window)
		return
	}
	
	container := gui.containers[gui.selectedIndex]
	if container.Status == "running" {
		dialog.ShowError(fmt.Errorf("cannot remove running container. Stop it first"), gui.window)
		return
	}
	
	// Confirm action
	dialog.ShowConfirm("Remove Container", 
		fmt.Sprintf("Are you sure you want to remove container %s?", container.ID[:12]),
		func(confirmed bool) {
			if confirmed {
				cmd := exec.Command("/proc/self/exe", "rm", container.ID)
				output, err := cmd.CombinedOutput()
				if err != nil {
					dialog.ShowError(fmt.Errorf("failed to remove container: %v\n%s", err, output), gui.window)
					return
				}
				
				dialog.ShowInformation("Container Removed", 
					fmt.Sprintf("Container %s removed successfully", container.ID[:12]), 
					gui.window)
				
				gui.refreshContainers()
				gui.logViewer.SetText("")
			}
		}, gui.window)
}

