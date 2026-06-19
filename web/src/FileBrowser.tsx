import { useEffect, useState } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { javascript } from "@codemirror/lang-javascript";
import { json } from "@codemirror/lang-json";
import { python } from "@codemirror/lang-python";
import { go } from "@codemirror/lang-go";
import { listDir, readFile, fileDownloadURL, type DirEntry, type FileContent } from "./api";

function langFor(name: string) {
  const ext = name.split(".").pop()?.toLowerCase();
  switch (ext) {
    case "ts": case "tsx": case "js": case "jsx": return [javascript({ jsx: true, typescript: true })];
    case "json": return [json()];
    case "py": return [python()];
    case "go": return [go()];
    default: return [];
  }
}

function joinPath(dir: string, name: string) { return dir ? `${dir}/${name}` : name; }
function parentPath(p: string) { const i = p.lastIndexOf("/"); return i < 0 ? "" : p.slice(0, i); }

export function FileBrowser({ agent, app }: { agent: string; app: string }) {
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState<DirEntry[]>([]);
  const [open, setOpen] = useState<FileContent | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let stop = false;
    setErr(null);
    listDir(agent, app, path)
      .then((l) => { if (!stop) setEntries(l.entries); })
      .catch((e) => { if (!stop) setErr(String(e.message || e)); });
    return () => { stop = true; };
  }, [agent, app, path]);

  async function onEntry(e: DirEntry) {
    if (e.is_dir) { setOpen(null); setPath(joinPath(path, e.name)); return; }
    setErr(null);
    try { setOpen(await readFile(agent, app, joinPath(path, e.name))); }
    catch (e2: any) { setErr(String(e2.message || e2)); }
  }

  const crumbs = path ? path.split("/") : [];
  return (
    <div className="filebrowser">
      <div className="fb-note">Viewing only — edits aren't supported yet; redeploy overwrites local changes.</div>
      <div className="crumb fb-crumb">
        <a href="#" onClick={(ev) => { ev.preventDefault(); setOpen(null); setPath(""); }}>{app}</a>
        {crumbs.map((c, i) => {
          const sub = crumbs.slice(0, i + 1).join("/");
          return <span key={sub}><span className="sep">/</span>
            <a href="#" onClick={(ev) => { ev.preventDefault(); setOpen(null); setPath(sub); }}>{c}</a></span>;
        })}
      </div>
      {err && <div className="fb-err">{err}</div>}
      <div className="fb-body">
        <ul className="fb-list">
          {path !== "" && (
            <li className="fb-row" onClick={() => { setOpen(null); setPath(parentPath(path)); }}>
              <span className="fb-name">../</span></li>
          )}
          {entries.map((e) => (
            <li key={e.name} className="fb-row" onClick={() => onEntry(e)}>
              <span className="fb-name">{e.is_dir ? "📁 " : "📄 "}{e.name}</span>
              <span className="fb-size">{e.is_dir ? "" : `${e.size} B`}</span>
            </li>
          ))}
        </ul>
        <div className="fb-view">
          {!open && <div className="fb-empty">Select a file to view.</div>}
          {open && open.binary && (
            <div className="fb-empty">
              Binary file ({open.size} B). <a href={fileDownloadURL(agent, app, open.path)} download>Download</a>
            </div>
          )}
          {open && !open.binary && (
            <>
              {open.truncated && <div className="fb-note">Showing first 1 MiB of {open.size} B. <a href={fileDownloadURL(agent, app, open.path)} download>Download full</a></div>}
              <CodeMirror value={open.content} editable={false} readOnly extensions={langFor(open.path)} theme="dark" />
            </>
          )}
        </div>
      </div>
    </div>
  );
}
