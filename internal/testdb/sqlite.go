// Package testdb provides an actual SQLite database behind the rqlite HTTP wire
// protocol for fast SQL integration tests. Appliance tests use real rqlite.
package testdb

import (
	"bufio"
	"bytes"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"os/exec"
	"path/filepath"
	"testing"
)

func Start(t *testing.T) *rqlite.Client {
	t.Helper()
	command := exec.Command("python3", "-u", "-c", server, filepath.Join(t.TempDir(), "test.sqlite"))
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("SQLite integration fixture requires Python 3: %v", err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("start SQLite fixture: %s", stderr.String())
	}
	return rqlite.NewClient(scanner.Text())
}

const server = `
import http.server, json, sqlite3, sys
connection = sqlite3.connect(sys.argv[1])
class Handler(http.server.BaseHTTPRequestHandler):
 def log_message(self, *args): pass
 def do_POST(self):
  statements=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  results=[]
  try:
   for statement in statements:
    try:
     cursor=connection.execute(statement[0],statement[1:])
     if cursor.description:
      result={'columns':[column[0] for column in cursor.description],'values':cursor.fetchall()}
     else:
      result={'rows_affected':max(0,cursor.rowcount),'last_insert_id':cursor.lastrowid}
     results.append(result)
    except sqlite3.Error as error:
     results.append({'error':str(error)});connection.rollback();break
   else: connection.commit()
   body=json.dumps({'results':results}).encode()
   self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(body)
  except Exception as error:
   self.send_error(500,str(error))
server=http.server.HTTPServer(('127.0.0.1',0),Handler)
print('http://127.0.0.1:'+str(server.server_port),flush=True)
server.serve_forever()
`
