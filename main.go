package main

import (
	"bufio"
	"fmt"
	"log/syslog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Domino timestamp format for time.Parse.
var timestampFormat string

// Thread IDs prepended to log lines.
var threadIDRegex = regexp.MustCompile(`^\[([A-Z\d:-]+)\]\s+`)

// Rest of the line -- optional timestamp and text message.
var timestampRegex = regexp.MustCompile(`^(\d\d\/\d\d\/\d\d\d\d\s+\d\d:\d\d:\d\d\s+[AP]M)\s+`)

// Number of seconds allowed between timestamp and current time before we log both.
const minAccuracy = 90 * time.Minute // 2 * time.Second

// Facility to use. I assume nobody needs Usenet on their Domino servers these days.
const facility = syslog.LOG_NEWS

const logTag = "domino"

// toUTF8 converts a string from ISO-8859-1 / Latin-1 legacy encoding to UTF-8.
func toUTF8(bytes []byte) string {
	utf8 := make([]rune, len(bytes))
	for i, b := range bytes {
		utf8[i] = rune(b)
	}
	return string(utf8)
}

func extractThreadID(data []byte) (string, []byte) {
	m := threadIDRegex.FindSubmatch(data)
	thread := ""
	rest := data
	if len(m) > 0 {
		thread = string(m[1])
		rest = data[len(m[0]):]
	}
	return thread, rest
}

func extractTimestamp(data []byte) (string, []byte) {
	m := timestampRegex.FindSubmatch(data)
	timestamp := ""
	rest := data
	if len(m) > 0 {
		stime := string(m[1])
		ts, err := time.ParseInLocation(timestampFormat, stime, time.Local)
		if err != nil {
			fmt.Fprintf(os.Stderr, "couldn't parse timestamp %s: %s\n", stime, err)
		} else {
			// If it's too far from now, record exactly what Domino emitted
			tdiff := time.Now().Sub(ts)
			if tdiff > minAccuracy {
				timestamp = string(m[1])
			}
		}
		rest = data[len(m[0]):]
	}
	return timestamp, rest
}

// Rule represents a rule which maps a regular expression to a syslog priority
// level.
type Rule struct {
	re  *regexp.Regexp
	lvl syslog.Priority
}

func NewRule(re string, lvl syslog.Priority) Rule {
	return Rule{regexp.MustCompile(re), lvl}
}

// Ideally the rules would be in a config file, but I rarely change them.
var rules = []Rule{
	NewRule("Access control is set in .* to not allow replication from", syslog.LOG_ERR),
	NewRule("Access control is set in .* to not replicate", syslog.LOG_WARNING),
	NewRule("not authorized to", syslog.LOG_WARNING),
	NewRule("Unable to find path to server.", syslog.LOG_CRIT),
	NewRule("No route is known from this host to ", syslog.LOG_CRIT),
	NewRule("The server is not responding", syslog.LOG_CRIT),
	NewRule("Server not reachable on Cluster Port", syslog.LOG_CRIT),
	NewRule("Full text operations on database .* which is not full text indexed", syslog.LOG_WARNING),
	NewRule("ATTEMPT TO ACCESS SERVER by .* was denied", syslog.LOG_ERR),
	NewRule("Directory Assistance could not", syslog.LOG_ERR),
	NewRule("Corrupt Data Exception", syslog.LOG_ERR),
	NewRule("Couldn't find design note", syslog.LOG_ERR),
	NewRule("\berror\b", syslog.LOG_ERR),
	NewRule("Warning:", syslog.LOG_WARNING),
}

// prioritize decides which syslog priority level to use, based on simple
// searches of the message against the rules.
func prioritize(msg string) syslog.Priority {
	for _, rule := range rules {
		if rule.re.MatchString(msg) {
			return rule.lvl
		}
	}
	return syslog.LOG_INFO
}

// process accepts a line of standard output from the Domino server,
// processes it, and writes the results to syslog.
func process(line []byte, slog *syslog.Writer) {
	rest := line
	// Sometimes Domino prefixes lines with "> "
	if len(rest) < 3 {
		return
	}
	if rest[0] == '>' && rest[1] == ' ' {
		rest = rest[2:]
	}
	threadid, rest := extractThreadID(rest)
	// Extract timestamp if found
	timestamp, rest := extractTimestamp(rest)
	// Sometimes Domino just prints empty lines
	if len(rest) < 1 {
		return
	}
	// And Domino still logs in Latin-1 even on Linux
	msg := toUTF8(rest)
	pri := prioritize(msg)
	if timestamp != "" {
		msg = fmt.Sprintf("%s (@ %s)", msg, timestamp)
	}
	if threadid != "" {
		msg = fmt.Sprintf("%s [%s]", msg, threadid)
	}
	var err error
	switch pri {
	case syslog.LOG_EMERG:
		err = slog.Emerg(msg)
	case syslog.LOG_ALERT:
		err = slog.Alert(msg)
	case syslog.LOG_CRIT:
		err = slog.Crit(msg)
	case syslog.LOG_ERR:
		err = slog.Err(msg)
	case syslog.LOG_WARNING:
		err = slog.Warning(msg)
	case syslog.LOG_NOTICE:
		err = slog.Notice(msg)
	default:
		err = slog.Info(msg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error writing to syslog: %s", err)
	}
}

// convertLogs reads line by line from the input scanner, writes processed
// log entries to the syslog, and when the input EOFs it closes the channel
// to indicate that the program can quit. Example of direct use:
//   scanner := bufio.NewScanner(os.Stdin)
//	 go convertLogs(scanner, logger, finished)
func convertLogs(scanner *bufio.Scanner, logger *syslog.Writer, done chan bool) {
	for scanner.Scan() {
		process(scanner.Bytes(), logger)
		os.Stdout.Write((scanner.Bytes()))
		os.Stdout.WriteString("\n")
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "error reading standard input:", err)
	}
	done <- true
}

// runCommand runs a Unix command, writing output from the command's stdout
// to the syslog, until the command closes its output stream.
func runCommand(cmdline []string, logger *syslog.Writer) error {
	cmdname := cmdline[0]
	var cmd *exec.Cmd
	if len(cmdline) > 1 {
		cmd = exec.Command(cmdname, cmdline[1:]...)
	} else {
		cmd = exec.Command(cmdname)
	}
	cmdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error getting pipe from %s: %s", cmdname, err)
	}

	scanner := bufio.NewScanner(cmdout)

	done := make(chan bool)
	go convertLogs(scanner, logger, done)

	fmt.Printf("Starting %s %v", cmdname, os.Args[1:])
	err = cmd.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error starting %s: %s", cmdname, err)
		return err
	}

	err = cmd.Wait()
	<-done
	if err != nil {
		fmt.Fprintf(os.Stderr, "error running %s: %s", cmdname, err)
	} else {
		fmt.Fprintf(os.Stderr, "successfully ran %s to completion", cmdname)
	}
	return err
}

func main() {

	// I only care about two locales, US and EN_DK (which is US with ISO dates)
	if strings.EqualFold(os.Getenv("LC_ALL"), "en_dk.utf-8") {
		timestampFormat = "2006/01/02 03:04:05 PM"
	} else {
		timestampFormat = "01/02/2006 03:04:05 PM"
	}

	logger, err := syslog.New(syslog.LOG_INFO, logTag)
	if err != nil {
		panic(err)
	}
	defer func() {
		cerr := logger.Close()
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "error closing syslog: %s", cerr)
		}
	}()

	if len(os.Args) > 2 && os.Args[1] == "run" {
		// Explicit command line
		runCommand(os.Args[2:], logger)
	} else {
		// Otherwise, pretend to be Domino and run Domino from its usual place.
		// Oddly, the Domino 'server' command is a shell script for unspecified
		// shell.
		args := []string{"/bin/sh", "/opt/ibm/domino/bin/server"}
		if len(os.Args) > 1 {
			// Append any arguments we were given
			args = append(args, os.Args[1:]...)
		}
		runCommand(args, logger)
	}

}
