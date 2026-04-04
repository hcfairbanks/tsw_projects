"""Track Claude API costs per call and write summaries to a timestamped log file.

Usage:
    from claude_cost_tracker import cost_tracker

    # Record a call (done automatically by call_llm)
    cost_tracker.record(task, usage)

    # Mark a section start
    cost_tracker.mark("service_71")

    # Print costs since last mark
    cost_tracker.print_since_mark("service_71")

    # The file is written to automatically on each record().
"""

import os
import time
from datetime import datetime

import config


# Pricing per 1M tokens (Claude Sonnet 4.6)
INPUT_PRICE = 3.0
OUTPUT_PRICE = 15.0


class CostEntry:
    __slots__ = ("timestamp", "task", "input_tokens", "output_tokens", "cost")

    def __init__(self, task, input_tokens, output_tokens):
        self.timestamp = time.time()
        self.task = task
        self.input_tokens = input_tokens
        self.output_tokens = output_tokens
        self.cost = (input_tokens / 1_000_000) * INPUT_PRICE + \
                    (output_tokens / 1_000_000) * OUTPUT_PRICE


class CostTracker:
    def __init__(self):
        self.entries = []
        self.marks = {}       # name -> index into self.entries (next entry after mark)
        self._log_path = None

    @property
    def log_path(self):
        if self._log_path is None:
            ts = datetime.now().strftime("%Y%m%d_%H%M%S")
            reports_dir = os.path.join(config.BASE_DIR, "claude_reports")
            os.makedirs(reports_dir, exist_ok=True)
            self._log_path = os.path.join(reports_dir, f"{ts}_claude.txt")
            # Write header
            with open(self._log_path, "w", encoding="utf-8") as f:
                f.write(f"Claude API Cost Log — {datetime.now():%Y-%m-%d %H:%M:%S}\n")
                f.write(f"Model: {config.CLAUDE_MODEL}\n")
                f.write(f"{'='*70}\n\n")
        return self._log_path

    def record(self, task, usage):
        """Record a Claude API call. usage is the message.usage object."""
        entry = CostEntry(task, usage.input_tokens, usage.output_tokens)
        self.entries.append(entry)
        self._append_to_file(entry)
        return entry

    def mark(self, name):
        """Set a named marker at the current position (next entry will be first after mark)."""
        self.marks[name] = len(self.entries)

    def entries_since_mark(self, name):
        """Return all entries recorded since the named mark."""
        start = self.marks.get(name, 0)
        return self.entries[start:]

    def cost_since_mark(self, name):
        """Return total cost since the named mark."""
        return sum(e.cost for e in self.entries_since_mark(name))

    def print_since_mark(self, name, label=None):
        """Print a cost breakdown for entries since a named mark."""
        entries = self.entries_since_mark(name)
        if not entries:
            return

        label = label or name
        total_input = sum(e.input_tokens for e in entries)
        total_output = sum(e.output_tokens for e in entries)
        total_cost = sum(e.cost for e in entries)

        lines = []
        lines.append(f"\n  ┌─── Claude Cost: {label} ───")
        for e in entries:
            lines.append(f"  │  {e.task:<20s}  "
                         f"in:{e.input_tokens:>7,}  out:{e.output_tokens:>6,}  "
                         f"${e.cost:.4f}")
        lines.append(f"  ├───────────────────────────────────────────")
        lines.append(f"  │  TOTAL  "
                     f"in:{total_input:>7,}  out:{total_output:>6,}  "
                     f"${total_cost:.4f}")
        lines.append(f"  └───────────────────────────────────────────")

        text = "\n".join(lines)
        print(text)

        # Also write the summary to the log file
        self._append_summary_to_file(label, entries, total_input, total_output, total_cost)

    def print_grand_total(self):
        """Print the grand total of all recorded calls."""
        if not self.entries:
            return
        total_input = sum(e.input_tokens for e in self.entries)
        total_output = sum(e.output_tokens for e in self.entries)
        total_cost = sum(e.cost for e in self.entries)

        print(f"\n{'='*50}")
        print(f"  GRAND TOTAL Claude API Cost: ${total_cost:.4f}")
        print(f"  Calls: {len(self.entries)}  "
              f"Tokens: {total_input:,} in / {total_output:,} out")
        print(f"{'='*50}")

        # Write grand total to file
        with open(self.log_path, "a", encoding="utf-8") as f:
            f.write(f"\n{'='*70}\n")
            f.write(f"GRAND TOTAL\n")
            f.write(f"  Calls:         {len(self.entries)}\n")
            f.write(f"  Input tokens:  {total_input:,}\n")
            f.write(f"  Output tokens: {total_output:,}\n")
            f.write(f"  Total cost:    ${total_cost:.4f}\n")

    def _append_to_file(self, entry):
        """Append a single call entry to the log file."""
        ts = datetime.fromtimestamp(entry.timestamp).strftime("%H:%M:%S")
        with open(self.log_path, "a", encoding="utf-8") as f:
            f.write(f"[{ts}] {entry.task:<20s}  "
                    f"in:{entry.input_tokens:>7,}  out:{entry.output_tokens:>6,}  "
                    f"${entry.cost:.4f}\n")

    def _append_summary_to_file(self, label, entries, total_input, total_output, total_cost):
        """Append a section summary to the log file."""
        with open(self.log_path, "a", encoding="utf-8") as f:
            f.write(f"\n--- Summary: {label} ---\n")
            for e in entries:
                ts = datetime.fromtimestamp(e.timestamp).strftime("%H:%M:%S")
                f.write(f"  [{ts}] {e.task:<20s}  "
                        f"in:{e.input_tokens:>7,}  out:{e.output_tokens:>6,}  "
                        f"${e.cost:.4f}\n")
            f.write(f"  TOTAL: in:{total_input:>7,}  out:{total_output:>6,}  "
                    f"${total_cost:.4f}\n")
            f.write(f"---\n\n")


# Singleton
cost_tracker = CostTracker()
