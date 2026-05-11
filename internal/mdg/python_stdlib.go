// internal/mdg/python_stdlib.go
//
// Bundled Python 3 stdlib module names. Sourced from
//   python3 -c "import sys; print('\n'.join(sorted(sys.stdlib_module_names)))"
// on CPython 3.13 plus a handful of long-standing names that predate
// stdlib_module_names. Maintained by hand; refresh when adding language
// support for newer Python versions.

package mdg

import "strings"

// pythonStdlib is the lookup set. Keys are top-level module names.
var pythonStdlib = map[string]struct{}{
	"__future__": {}, "_thread": {}, "abc": {}, "aifc": {}, "argparse": {},
	"array": {}, "ast": {}, "asynchat": {}, "asyncio": {}, "asyncore": {},
	"atexit": {}, "audioop": {}, "base64": {}, "bdb": {}, "binascii": {},
	"bisect": {}, "builtins": {}, "bz2": {}, "cProfile": {}, "calendar": {},
	"cgi": {}, "cgitb": {}, "chunk": {}, "cmath": {}, "cmd": {},
	"code": {}, "codecs": {}, "codeop": {}, "collections": {}, "colorsys": {},
	"compileall": {}, "concurrent": {}, "configparser": {}, "contextlib": {},
	"contextvars": {}, "copy": {}, "copyreg": {}, "crypt": {}, "csv": {},
	"ctypes": {}, "curses": {}, "dataclasses": {}, "datetime": {}, "dbm": {},
	"decimal": {}, "difflib": {}, "dis": {}, "distutils": {}, "doctest": {},
	"email": {}, "encodings": {}, "ensurepip": {}, "enum": {}, "errno": {},
	"faulthandler": {}, "fcntl": {}, "filecmp": {}, "fileinput": {}, "fnmatch": {},
	"fractions": {}, "ftplib": {}, "functools": {}, "gc": {}, "genericpath": {},
	"getopt": {}, "getpass": {}, "gettext": {}, "glob": {}, "graphlib": {},
	"grp": {}, "gzip": {}, "hashlib": {}, "heapq": {}, "hmac": {},
	"html": {}, "http": {}, "idlelib": {}, "imaplib": {}, "imghdr": {},
	"imp": {}, "importlib": {}, "inspect": {}, "io": {}, "ipaddress": {},
	"itertools": {}, "json": {}, "keyword": {}, "lib2to3": {}, "linecache": {},
	"locale": {}, "logging": {}, "lzma": {}, "mailbox": {}, "mailcap": {},
	"marshal": {}, "math": {}, "mimetypes": {}, "mmap": {}, "modulefinder": {},
	"msilib": {}, "msvcrt": {}, "multiprocessing": {}, "netrc": {}, "nis": {},
	"nntplib": {}, "ntpath": {}, "numbers": {}, "opcode": {}, "operator": {},
	"optparse": {}, "os": {}, "ossaudiodev": {}, "pathlib": {}, "pdb": {},
	"pickle": {}, "pickletools": {}, "pipes": {}, "pkgutil": {}, "platform": {},
	"plistlib": {}, "poplib": {}, "posix": {}, "posixpath": {}, "pprint": {},
	"profile": {}, "pstats": {}, "pty": {}, "pwd": {}, "py_compile": {},
	"pyclbr": {}, "pydoc": {}, "pydoc_data": {}, "pyexpat": {}, "queue": {},
	"quopri": {}, "random": {}, "re": {}, "readline": {}, "reprlib": {},
	"resource": {}, "rlcompleter": {}, "runpy": {}, "sched": {}, "secrets": {},
	"select": {}, "selectors": {}, "shelve": {}, "shlex": {}, "shutil": {},
	"signal": {}, "site": {}, "smtpd": {}, "smtplib": {}, "sndhdr": {},
	"socket": {}, "socketserver": {}, "spwd": {}, "sqlite3": {}, "sre_compile": {},
	"sre_constants": {}, "sre_parse": {}, "ssl": {}, "stat": {}, "statistics": {},
	"string": {}, "stringprep": {}, "struct": {}, "subprocess": {}, "sunau": {},
	"symtable": {}, "sys": {}, "sysconfig": {}, "syslog": {}, "tabnanny": {},
	"tarfile": {}, "telnetlib": {}, "tempfile": {}, "termios": {}, "test": {},
	"textwrap": {}, "threading": {}, "time": {}, "timeit": {}, "tkinter": {},
	"token": {}, "tokenize": {}, "tomllib": {}, "trace": {}, "traceback": {},
	"tracemalloc": {}, "tty": {}, "turtle": {}, "turtledemo": {}, "types": {},
	"typing": {}, "unicodedata": {}, "unittest": {}, "urllib": {}, "uu": {},
	"uuid": {}, "venv": {}, "warnings": {}, "wave": {}, "weakref": {},
	"webbrowser": {}, "winreg": {}, "winsound": {}, "wsgiref": {}, "xdrlib": {},
	"xml": {}, "xmlrpc": {}, "zipapp": {}, "zipfile": {}, "zipimport": {},
	"zlib": {}, "zoneinfo": {},
}

// isPythonStdlib reports whether the dotted Python module name belongs to
// the standard library. Multi-segment names match against the top segment:
// "os.path" → look up "os".
func isPythonStdlib(name string) bool {
	if name == "" {
		return false
	}
	top := name
	if i := strings.IndexByte(name, '.'); i >= 0 {
		top = name[:i]
	}
	_, ok := pythonStdlib[top]
	return ok
}
