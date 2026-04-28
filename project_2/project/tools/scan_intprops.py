"""Scan the base64 payload of a TSW6 timetable uasset.json and dump
every IntProperty value per service, then compare against bot ground
truth to find a bit that distinguishes conductor-compatible services.
"""
import base64
import json
import struct
import sys
from pathlib import Path


def read_u8(d, p):
    return d[p], p + 1


def read_i32(d, p):
    return struct.unpack_from("<i", d, p)[0], p + 4


def read_i64(d, p):
    return struct.unpack_from("<q", d, p)[0], p + 8


def fname(d, p, namemap):
    idx, p = read_i32(d, p)
    num, p = read_i32(d, p)
    if idx < 0 or idx >= len(namemap):
        return f"?{idx}", p
    s = namemap[idx]
    if num > 0:
        s = f"{s}_{num-1}"
    return s, p


def read_tag(d, p, namemap):
    name, p = fname(d, p, namemap)
    if name == "None":
        return None, p
    ptype, p = fname(d, p, namemap)
    size, p = read_i32(d, p)
    p += 4  # arr_idx
    struct_type = ""
    inner_type = ""
    bool_val = 0
    if ptype == "StructProperty":
        struct_type, p = fname(d, p, namemap)
        p += 16  # guid
    elif ptype == "BoolProperty":
        bool_val, p = read_u8(d, p)
    elif ptype in ("ByteProperty", "EnumProperty", "ArrayProperty"):
        inner_type, p = fname(d, p, namemap)
    elif ptype == "MapProperty":
        _, p = fname(d, p, namemap)
        _, p = fname(d, p, namemap)
    elif ptype == "SetProperty":
        _, p = fname(d, p, namemap)
    has_guid, p = read_u8(d, p)
    if has_guid:
        p += 16
    return {
        "name": name,
        "ptype": ptype,
        "size": size,
        "struct": struct_type,
        "inner": inner_type,
        "bool": bool_val,
    }, p


def fstr(d, p):
    n, p = read_i32(d, p)
    if n == 0:
        return "", p
    if n > 0:
        s = d[p:p + n - 1].decode("latin-1", errors="replace")
        return s, p + n
    n = -n
    s = d[p:p + n * 2].decode("utf-16-le", errors="replace").rstrip("\x00")
    return s, p + n * 2


def ftext(d, p, size):
    end = p + size
    if size < 5:
        return "", end
    p += 4  # flags
    history, p = read_u8(d, p)
    if history == 0xFF:
        if p >= end:
            return "", end
        has, p = read_u8(d, p)
        if has:
            s, p = fstr(d, p)
            return s, end
        return "", end
    if history == 0:
        _, p = fstr(d, p)
        _, p = fstr(d, p)
        s, p = fstr(d, p)
        return s, end
    return "", end


def scan_service(d, start, limit, namemap):
    """Walk a service struct and collect every IntProperty and bit-carrying
    property. Return dict."""
    p = start
    props = {}
    while p < limit:
        tag, p2 = read_tag(d, p, namemap)
        if tag is None:
            return props, p2
        p = p2
        dp = p
        name = tag["name"]
        ptype = tag["ptype"]
        size = tag["size"]

        if ptype == "NameProperty":
            v, _ = fname(d, p, namemap)
            props[name] = v
        elif ptype == "StrProperty":
            v, _ = fstr(d, p)
            props[name] = v
        elif ptype == "BoolProperty":
            props[name] = bool(tag["bool"])
        elif ptype == "IntProperty" and size == 4:
            v, _ = read_i32(d, p)
            props[name] = v
        elif ptype == "EnumProperty":
            v, _ = fname(d, p, namemap)
            props[name] = v
        elif ptype == "TextProperty":
            v, _ = ftext(d, p, size)
            props[name] = v
        elif ptype == "StructProperty" and tag["struct"] == "Timespan":
            v, _ = read_i64(d, p)
            props[name] = v

        p = dp + size
    return props, p


def parse_services(d, namemap):
    p = 0
    services = []
    while p < len(d) - 8:
        tag, p = read_tag(d, p, namemap)
        if tag is None:
            break
        if tag["name"] == "Services" and tag["ptype"] == "ArrayProperty":
            # services array
            start = p
            count, p = read_i32(d, p)
            if count == 0:
                return services
            inner, p = read_tag(d, p, namemap)
            arr_end = p + inner["size"]
            for _ in range(count):
                if p >= arr_end:
                    break
                props, p = scan_service(d, p, arr_end, namemap)
                services.append(props)
            return services
        p += tag["size"]
    return services


def main():
    json_path = sys.argv[1]
    with open(json_path, encoding="utf-8") as f:
        doc = json.load(f)
    namemap = doc["NameMap"]
    b64 = doc["Exports"][0]["Data"]
    payload = base64.b64decode(b64)
    services = parse_services(payload, namemap)
    print(f"Parsed {len(services)} services", file=sys.stderr)

    for svc in services:
        print(json.dumps(svc, default=str))


if __name__ == "__main__":
    main()
