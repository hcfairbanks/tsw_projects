import re
import csv
import sys

def normalize_time(t):
    """Ensure time is in HH:MM:SS format."""
    parts = t.strip().split(':')
    if len(parts) == 2:
        return f"{parts[0].zfill(2)}:{parts[1].zfill(2)}:00"
    elif len(parts) == 3:
        return f"{parts[0].zfill(2)}:{parts[1].zfill(2)}:{parts[2].zfill(2)}"
    return t

def convert(input_file, output_file=None):
    if output_file is None:
        output_file = input_file.rsplit('.', 1)[0] + '.csv'

    pattern = re.compile(
        r'#\s*(\d+)\s+page\s+(\d+)\s+click\s+\((\d+),\s*(\d+)\)\s+'
        r'(.+?)\s+-\s+(.+?)\s+\|\s+([\d:]+)\s+\|\s+([\d:]+)'
    )

    count = 0
    with open(input_file, 'r') as infile, open(output_file, 'w', newline='') as outfile:
        writer = csv.writer(outfile)
        writer.writerow(['number', 'page', 'click', 'service_id', 'service_name', 'time', 'duration'])

        for line in infile:
            line = line.strip()
            if not line:
                continue
            m = pattern.search(line)
            if m:
                num = int(m.group(1))
                page = int(m.group(2))
                click_x = int(m.group(3))
                click_y = int(m.group(4))
                service_id = m.group(5).strip()
                route = m.group(6).strip()
                time_val = normalize_time(m.group(7))
                duration = normalize_time(m.group(8))
                writer.writerow([num, f'page {page}', f'click ({click_x} {click_y})', service_id, route, time_val, duration])
                count += 1

    print(f"CSV written to {output_file} with {count} entries")

if __name__ == '__main__':
    if len(sys.argv) < 2:
        print("Usage: python format_services_csv.py <input_file> [output_file]")
        sys.exit(1)

    input_file = sys.argv[1]
    output_file = sys.argv[2] if len(sys.argv) > 2 else None
    convert(input_file, output_file)
