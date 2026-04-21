package tui

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	// Premium Palette - Deep Dark / Obsidian
	primaryColor   = lipgloss.Color("#9B59B6") // Amethyst
	secondaryColor = lipgloss.Color("#2ECC71") // Emerald
	textColor      = lipgloss.Color("#ECF0F1") // Clouds
	subtextColor   = lipgloss.Color("#95A5A6") // Concrete
	warningColor   = lipgloss.Color("#F1C40F") // Sunflower/Yellow

	// Message Backgrounds
	userMsgBg      = lipgloss.Color("#16222A") // Very dark blue/black
	aiMsgBg        = lipgloss.Color("#191919") // Almost black, slightly lighter than terminal
	thoughtBgColor = lipgloss.Color("#101010") // Near black

	// Layout Constants
	UserMsgOverhead = 7 // Margin(1)*2 + Border(1) + Padding(2)*2 = 7
	AIMsgOverhead   = 9 // Margin(1)*2 + Border(1) + PaddingL(4) + PaddingR(2) = 9

	// Styles
	appStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#191919")).
			Foreground(textColor)

	inputStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(primaryColor).
			BorderBackground(aiMsgBg).
			Padding(0, 1).
			Background(aiMsgBg)

	// User Bubble
	userMsgStyle = lipgloss.NewStyle().
			Background(userMsgBg).
			Foreground(textColor).
			Padding(0, 2).
			Margin(0, 1).
			Align(lipgloss.Left).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderLeftForeground(secondaryColor).
			BorderBackground(userMsgBg).
			PaddingLeft(2)

	// AI Bubble
	aiMsgStyle = lipgloss.NewStyle().
			Background(aiMsgBg).
			Padding(0, 2).
			Margin(0, 1).
			PaddingLeft(4).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderLeftForeground(primaryColor).
			BorderBackground(aiMsgBg)

	// Thinking Block
	thinkingStyle = lipgloss.NewStyle().
			Foreground(subtextColor).
			Background(thoughtBgColor).
			Italic(true).
			Padding(0, 1).
			MarginLeft(4).
			BorderLeft(true).
			BorderStyle(lipgloss.ThickBorder()).
			BorderForeground(lipgloss.Color("#555555")).
			BorderBackground(thoughtBgColor)

	tagStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true).
			Background(thoughtBgColor).
			MarginLeft(1).
			PaddingLeft(1)

	statusBarBaseStyle = lipgloss.NewStyle().
				Height(StatusBarHeight).
				Background(lipgloss.Color("#121212")).
				Foreground(textColor)

	statusModeStyle = lipgloss.NewStyle().
			Background(primaryColor).
			Foreground(textColor).
			Padding(0, 1).
			Bold(true).
			MarginRight(1)

	statusKeyStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true)

	statusTextStyle = lipgloss.NewStyle().
			Foreground(subtextColor).
			MarginLeft(1)

	statusWarningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#121212")).
				Background(warningColor).
				Bold(true).
				Padding(0, 1).
				MarginLeft(1)
)
