package utils

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/sirupsen/logrus"
)

type LogFormatter struct {
}

func InitLogger() {
	color.NoColor = false
	logrus.SetLevel(logrus.InfoLevel)
	logrus.SetReportCaller(true)
	logrus.SetFormatter(&LogFormatter{})
}
func (s *LogFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}

	var levelColor *color.Color
	switch entry.Level {
	case logrus.PanicLevel:
		levelColor = color.New(color.FgRed)
	case logrus.FatalLevel:
		levelColor = color.New(color.FgRed)
	case logrus.ErrorLevel:
		levelColor = color.New(color.FgRed)
	case logrus.WarnLevel:
		levelColor = color.New(color.FgYellow)
	case logrus.InfoLevel:
		levelColor = color.New(color.FgGreen)
	case logrus.DebugLevel:
		levelColor = color.New(color.FgCyan)
	case logrus.TraceLevel:
		levelColor = color.New(color.FgBlue)
	default:
		levelColor = color.New(color.FgWhite)
	}
	level := levelColor.Add(color.Bold).SprintFunc()(strings.ToUpper(entry.Level.String()))
	lang := color.New(color.FgHiMagenta).Add(color.Bold).SprintFunc()("Go")
	timestamp := entry.Time.Format("2006-01-02 15:04:05")
	var file string
	if entry.HasCaller() {
		file = fmt.Sprintf("%s:%d", entry.Caller.File, entry.Caller.Line)
	}
	msg := fmt.Sprintf("%s %s [%s] %s - %s\n", lang, timestamp, level, file, entry.Message)
	b.WriteString(msg)
	return b.Bytes(), nil
}
