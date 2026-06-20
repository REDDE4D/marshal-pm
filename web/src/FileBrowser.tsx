import { useEffect, useState } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { javascript } from "@codemirror/lang-javascript";
import { json } from "@codemirror/lang-json";
import { python } from "@codemirror/lang-python";
import { go } from "@codemirror/lang-go";
import {
  listDir, readFile, fileDownloadURL, writeFile, deleteFile, renameFile,
  type DirEntry, type FileContent,
} from "./api";

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

export function FileBrowser({ agent, app, credential }: { agent: string; app: string; credential?: string }) {
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState<DirEntry[]>([]);
  const [open, setOpen] = useState<FileContent | null>(null);
  const [draft, setDraft] = useState("");          // editor buffer
  const [msg, setMsg] = useState("");              // commit message
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let stop = false;
    setErr(null);
    listDir(agent, app, path)
      .then((l) => { if (!stop) setEntries(l.entries); })
      .catch((e) => { if (!stop) setErr(String(e.message || e)); });
    return () => { stop = true; };
  }, [agent, app, path, reload]);

  async function onEntry(e: DirEntry) {
    if (e.is_dir) { setOpen(null); setPath(joinPath(path, e.name)); return; }
    setErr(null); setNote(null);
    try {
      const f = await readFile(agent, app, joinPath(path, e.name));
      setOpen(f); setDraft(f.content); setMsg(`Update ${f.path}`);
    } catch (e2: any) { setErr(String(e2.message || e2)); }
  }

  const editable = !!open && !open.binary && !open.truncated;

  async function onSave() {
    if (!open) return;
    setBusy(true); setErr(null); setNote(null);
    try {
      const res = await writeFile(agent, app, open.path, draft, msg || `Update ${open.path}`, credential);
      setNote(`Pushed ${res.sha.slice(0, 7)} to ${res.branch}`);
      setOpen({ ...open, content: draft });
      setReload((n) => n + 1);
    } catch (e: any) { setErr(String(e.message || e)); }
    finally { setBusy(false); }
  }

  async function onNewFile() {
    const name = window.prompt("New file path (relative to current folder):");
    if (!name) return;
    const rel = joinPath(path, name);
    setBusy(true); setErr(null); setNote(null);
    try {
      const res = await writeFile(agent, app, rel, "", `Create ${rel}`, credential);
      setNote(`Pushed ${res.sha.slice(0, 7)} to ${res.branch}`);
      setReload((n) => n + 1);
    } catch (e: any) { setErr(String(e.message || e)); }
    finally { setBusy(false); }
  }

  async function onDelete(e: DirEntry) {
    const rel = joinPath(path, e.name);
    if (!window.confirm(`Delete ${rel}? This commits and pushes the deletion.`)) return;
    setBusy(true); setErr(null); setNote(null);
    try {
      const res = await deleteFile(agent, app, rel, `Delete ${rel}`, credential);
      setNote(`Pushed ${res.sha.slice(0, 7)} to ${res.branch}`);
      if (open?.path === rel) setOpen(null);
      setReload((n) => n + 1);
    } catch (e2: any) { setErr(String(e2.message || e2)); }
    finally { setBusy(false); }
  }

  async function onRename(e: DirEntry) {
    const to = window.prompt(`Rename ${e.name} to:`, e.name);
    if (!to || to === e.name) return;
    const from = joinPath(path, e.name);
    const dest = joinPath(path, to);
    setBusy(true); setErr(null); setNote(null);
    try {
      const res = await renameFile(agent, app, from, dest, `Rename ${from} → ${dest}`, credential);
      setNote(`Pushed ${res.sha.slice(0, 7)} to ${res.branch}`);
      if (open?.path === from) setOpen(null);
      setReload((n) => n + 1);
    } catch (e2: any) { setErr(String(e2.message || e2)); }
    finally { setBusy(false); }
  }

  const crumbs = path ? path.split("/") : [];
  return (
    <div className="filebrowser">
      <div className="fb-note">
        Editing commits &amp; pushes to origin per change. Redeploy to apply changes to the running app.
      </div>
      <div className="crumb fb-crumb">
        <a href="#" onClick={(ev) => { ev.preventDefault(); setOpen(null); setPath(""); }}>{app}</a>
        {crumbs.map((c, i) => {
          const sub = crumbs.slice(0, i + 1).join("/");
          return <span key={sub}><span className="sep">/</span>
            <a href="#" onClick={(ev) => { ev.preventDefault(); setOpen(null); setPath(sub); }}>{c}</a></span>;
        })}
        <button className="fb-action" disabled={busy} onClick={onNewFile} style={{ marginLeft: "auto" }}>+ New file</button>
      </div>
      {err && <div className="fb-err">{err}</div>}
      {note && <div className="fb-note">{note}</div>}
      <div className="fb-body">
        <ul className="fb-list">
          {path !== "" && (
            <li className="fb-row" onClick={() => { setOpen(null); setPath(parentPath(path)); }}>
              <span className="fb-name">../</span></li>
          )}
          {entries.map((e) => (
            <li key={e.name} className="fb-row">
              <span className="fb-name" onClick={() => onEntry(e)}>{e.is_dir ? "📁 " : "📄 "}{e.name}</span>
              <span className="fb-size">{e.is_dir ? "" : `${e.size} B`}</span>
              <span className="fb-rowactions">
                <button className="fb-action" disabled={busy} onClick={() => onRename(e)}>Rename</button>
                {!e.is_dir && <button className="fb-action" disabled={busy} onClick={() => onDelete(e)}>Delete</button>}
              </span>
            </li>
          ))}
        </ul>
        <div className="fb-view">
          {!open && <div className="fb-empty">Select a file to view or edit.</div>}
          {open && open.binary && (
            <div className="fb-empty">
              Binary file ({open.size} B). <a href={fileDownloadURL(agent, app, open.path)} download>Download</a>
            </div>
          )}
          {open && !open.binary && (
            <>
              {open.truncated && <div className="fb-note">Showing first 1 MiB of {open.size} B — too large to edit. <a href={fileDownloadURL(agent, app, open.path)} download>Download first 1 MiB</a></div>}
              <CodeMirror value={editable ? draft : open.content} editable={editable} readOnly={!editable}
                onChange={editable ? setDraft : undefined} extensions={langFor(open.path)} theme="dark" />
              {editable && (
                <div className="fb-saverow">
                  <input className="fb-msg" value={msg} onChange={(e) => setMsg(e.target.value)} placeholder="Commit message" />
                  <button className="fb-action" disabled={busy || draft === open.content} onClick={onSave}>Save &amp; push</button>
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
