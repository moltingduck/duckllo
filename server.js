const express = require('express');
const Database = require('better-sqlite3');
const bcrypt = require('bcryptjs');
const { v4: uuidv4 } = require('uuid');
const multer = require('multer');
const path = require('path');
const fs = require('fs');

const app = express();
const PORT = process.env.PORT || 3000;

// Ensure uploads directory exists
const uploadsDir = path.join(__dirname, 'uploads');
if (!fs.existsSync(uploadsDir)) fs.mkdirSync(uploadsDir);

// Multer config for GIF uploads
const storage = multer.diskStorage({
  destination: (req, file, cb) => cb(null, uploadsDir),
  filename: (req, file, cb) => {
    const ext = path.extname(file.originalname);
    cb(null, `${uuidv4()}${ext}`);
  }
});
const upload = multer({
  storage,
  limits: { fileSize: 50 * 1024 * 1024 }, // 50MB
  fileFilter: (req, file, cb) => {
    const allowed = ['.gif', '.png', '.jpg', '.jpeg', '.webp', '.mp4'];
    const ext = path.extname(file.originalname).toLowerCase();
    cb(null, allowed.includes(ext));
  }
});

app.use(express.json());
app.use(express.static(path.join(__dirname, 'public')));
app.use('/uploads', express.static(uploadsDir));

// ── Database Setup ──────────────────────────────────────────────────────

const db = new Database(path.join(__dirname, 'duckllo.db'));
db.pragma('journal_mode = WAL');
db.pragma('foreign_keys = ON');

db.exec(`
  CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    display_name TEXT,
    created_at TEXT DEFAULT (datetime('now'))
  );

  CREATE TABLE IF NOT EXISTS projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT,
    owner_id TEXT NOT NULL REFERENCES users(id),
    columns_config TEXT DEFAULT '["Backlog","Todo","In Progress","Review","Done"]',
    created_at TEXT DEFAULT (datetime('now'))
  );

  CREATE TABLE IF NOT EXISTS project_members (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'member',
    PRIMARY KEY (project_id, user_id)
  );

  CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    key_hash TEXT NOT NULL,
    key_prefix TEXT NOT NULL,
    label TEXT,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    permissions TEXT DEFAULT '["read","write"]',
    created_at TEXT DEFAULT (datetime('now'))
  );

  CREATE TABLE IF NOT EXISTS cards (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    description TEXT,
    card_type TEXT NOT NULL DEFAULT 'feature',
    column_name TEXT NOT NULL DEFAULT 'Backlog',
    position INTEGER NOT NULL DEFAULT 0,
    priority TEXT DEFAULT 'medium',
    assignee_id TEXT REFERENCES users(id),
    testing_status TEXT DEFAULT 'untested',
    testing_result TEXT,
    demo_gif_url TEXT,
    labels TEXT DEFAULT '[]',
    created_by TEXT REFERENCES users(id),
    created_at TEXT DEFAULT (datetime('now')),
    updated_at TEXT DEFAULT (datetime('now'))
  );

  CREATE TABLE IF NOT EXISTS card_comments (
    id TEXT PRIMARY KEY,
    card_id TEXT NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    user_id TEXT REFERENCES users(id),
    content TEXT NOT NULL,
    comment_type TEXT DEFAULT 'comment',
    created_at TEXT DEFAULT (datetime('now'))
  );

  CREATE TABLE IF NOT EXISTS sessions (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL
  );
`);

// ── Auth Middleware ──────────────────────────────────────────────────────

function authenticate(req, res, next) {
  // Check for API key (Bearer token starting with "duckllo_")
  const authHeader = req.headers.authorization;
  if (authHeader) {
    const token = authHeader.replace('Bearer ', '');

    if (token.startsWith('duckllo_')) {
      // API key auth
      const keys = db.prepare('SELECT ak.*, u.id as uid, u.username FROM api_keys ak JOIN users u ON ak.user_id = u.id').all();
      for (const key of keys) {
        if (bcrypt.compareSync(token, key.key_hash)) {
          req.user = { id: key.uid, username: key.username };
          req.apiKeyProjectId = key.project_id;
          req.apiKeyPermissions = JSON.parse(key.permissions);
          return next();
        }
      }
      return res.status(401).json({ error: 'Invalid API key' });
    }

    // Session token auth
    const session = db.prepare(
      "SELECT s.*, u.id as uid, u.username, u.display_name FROM sessions s JOIN users u ON s.user_id = u.id WHERE s.token = ? AND s.expires_at > datetime('now')"
    ).get(token);
    if (session) {
      req.user = { id: session.uid, username: session.username, display_name: session.display_name };
      return next();
    }
  }

  return res.status(401).json({ error: 'Authentication required' });
}

function requireProjectAccess(req, res, next) {
  const projectId = req.params.projectId || req.body.project_id;
  if (!projectId) return res.status(400).json({ error: 'Project ID required' });

  // If using API key, check project match
  if (req.apiKeyProjectId && req.apiKeyProjectId !== projectId) {
    return res.status(403).json({ error: 'API key not authorized for this project' });
  }

  const project = db.prepare('SELECT * FROM projects WHERE id = ?').get(projectId);
  if (!project) return res.status(404).json({ error: 'Project not found' });

  const isMember = db.prepare('SELECT * FROM project_members WHERE project_id = ? AND user_id = ?').get(projectId, req.user.id);
  if (!isMember && project.owner_id !== req.user.id) {
    return res.status(403).json({ error: 'Not a member of this project' });
  }

  req.project = project;
  req.memberRole = isMember ? isMember.role : (project.owner_id === req.user.id ? 'owner' : null);
  next();
}

// ── Auth Routes ─────────────────────────────────────────────────────────

app.post('/api/auth/register', (req, res) => {
  const { username, password, display_name } = req.body;
  if (!username || !password) return res.status(400).json({ error: 'Username and password required' });
  if (username.length < 3) return res.status(400).json({ error: 'Username must be at least 3 characters' });
  if (password.length < 6) return res.status(400).json({ error: 'Password must be at least 6 characters' });

  const existing = db.prepare('SELECT id FROM users WHERE username = ?').get(username);
  if (existing) return res.status(409).json({ error: 'Username already exists' });

  const id = uuidv4();
  const password_hash = bcrypt.hashSync(password, 10);
  db.prepare('INSERT INTO users (id, username, password_hash, display_name) VALUES (?, ?, ?, ?)').run(id, username, password_hash, display_name || username);

  const token = uuidv4();
  const expires = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString();
  db.prepare('INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)').run(token, id, expires);

  res.json({ token, user: { id, username, display_name: display_name || username } });
});

app.post('/api/auth/login', (req, res) => {
  const { username, password } = req.body;
  const user = db.prepare('SELECT * FROM users WHERE username = ?').get(username);
  if (!user || !bcrypt.compareSync(password, user.password_hash)) {
    return res.status(401).json({ error: 'Invalid credentials' });
  }

  const token = uuidv4();
  const expires = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString();
  db.prepare('INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)').run(token, user.id, expires);

  res.json({ token, user: { id: user.id, username: user.username, display_name: user.display_name } });
});

app.get('/api/auth/me', authenticate, (req, res) => {
  res.json({ user: req.user });
});

app.post('/api/auth/logout', authenticate, (req, res) => {
  const token = req.headers.authorization?.replace('Bearer ', '');
  db.prepare('DELETE FROM sessions WHERE token = ?').run(token);
  res.json({ ok: true });
});

// ── Project Routes ──────────────────────────────────────────────────────

app.get('/api/projects', authenticate, (req, res) => {
  const projects = db.prepare(`
    SELECT DISTINCT p.* FROM projects p
    LEFT JOIN project_members pm ON p.id = pm.project_id
    WHERE p.owner_id = ? OR pm.user_id = ?
    ORDER BY p.created_at DESC
  `).all(req.user.id, req.user.id);
  res.json(projects.map(p => ({ ...p, columns_config: JSON.parse(p.columns_config) })));
});

app.post('/api/projects', authenticate, (req, res) => {
  const { name, description, columns } = req.body;
  if (!name) return res.status(400).json({ error: 'Project name required' });

  const id = uuidv4();
  const columns_config = JSON.stringify(columns || ['Backlog', 'Todo', 'In Progress', 'Review', 'Done']);
  db.prepare('INSERT INTO projects (id, name, description, owner_id, columns_config) VALUES (?, ?, ?, ?, ?)').run(id, name, description || '', req.user.id, columns_config);
  db.prepare('INSERT INTO project_members (project_id, user_id, role) VALUES (?, ?, ?)').run(id, req.user.id, 'owner');

  res.json({ id, name, description, owner_id: req.user.id, columns_config: JSON.parse(columns_config) });
});

app.get('/api/projects/:projectId', authenticate, requireProjectAccess, (req, res) => {
  const project = { ...req.project, columns_config: JSON.parse(req.project.columns_config) };
  const members = db.prepare(`
    SELECT u.id, u.username, u.display_name, pm.role FROM project_members pm
    JOIN users u ON pm.user_id = u.id WHERE pm.project_id = ?
  `).all(req.params.projectId);
  res.json({ ...project, members });
});

app.post('/api/projects/:projectId/members', authenticate, requireProjectAccess, (req, res) => {
  if (req.memberRole !== 'owner') return res.status(403).json({ error: 'Only owner can add members' });

  const { username, role } = req.body;
  const user = db.prepare('SELECT id FROM users WHERE username = ?').get(username);
  if (!user) return res.status(404).json({ error: 'User not found' });

  const existing = db.prepare('SELECT * FROM project_members WHERE project_id = ? AND user_id = ?').get(req.params.projectId, user.id);
  if (existing) return res.status(409).json({ error: 'User is already a member' });

  db.prepare('INSERT INTO project_members (project_id, user_id, role) VALUES (?, ?, ?)').run(req.params.projectId, user.id, role || 'member');
  res.json({ ok: true });
});

// ── API Key Routes ──────────────────────────────────────────────────────

app.get('/api/projects/:projectId/api-keys', authenticate, requireProjectAccess, (req, res) => {
  const keys = db.prepare('SELECT id, key_prefix, label, permissions, created_at FROM api_keys WHERE project_id = ? AND user_id = ?').all(req.params.projectId, req.user.id);
  res.json(keys.map(k => ({ ...k, permissions: JSON.parse(k.permissions) })));
});

app.post('/api/projects/:projectId/api-keys', authenticate, requireProjectAccess, (req, res) => {
  const { label, permissions } = req.body;
  const rawKey = `duckllo_${uuidv4().replace(/-/g, '')}`;
  const id = uuidv4();
  const key_hash = bcrypt.hashSync(rawKey, 10);
  const key_prefix = rawKey.substring(0, 15) + '...';

  db.prepare('INSERT INTO api_keys (id, key_hash, key_prefix, label, user_id, project_id, permissions) VALUES (?, ?, ?, ?, ?, ?, ?)').run(
    id, key_hash, key_prefix, label || 'Agent Key', req.user.id, req.params.projectId, JSON.stringify(permissions || ['read', 'write'])
  );

  // Return the raw key only once
  res.json({ id, key: rawKey, key_prefix, label: label || 'Agent Key' });
});

app.delete('/api/projects/:projectId/api-keys/:keyId', authenticate, requireProjectAccess, (req, res) => {
  db.prepare('DELETE FROM api_keys WHERE id = ? AND project_id = ? AND user_id = ?').run(req.params.keyId, req.params.projectId, req.user.id);
  res.json({ ok: true });
});

// ── Card Routes ─────────────────────────────────────────────────────────

app.get('/api/projects/:projectId/cards', authenticate, requireProjectAccess, (req, res) => {
  const cards = db.prepare(`
    SELECT c.*, u.username as assignee_username, u.display_name as assignee_display_name,
           cr.username as creator_username
    FROM cards c
    LEFT JOIN users u ON c.assignee_id = u.id
    LEFT JOIN users cr ON c.created_by = cr.id
    WHERE c.project_id = ?
    ORDER BY c.position ASC
  `).all(req.params.projectId);
  res.json(cards.map(c => ({ ...c, labels: JSON.parse(c.labels) })));
});

app.post('/api/projects/:projectId/cards', authenticate, requireProjectAccess, (req, res) => {
  const { title, description, card_type, column_name, priority, assignee_id, labels } = req.body;
  if (!title) return res.status(400).json({ error: 'Title required' });

  const columns = JSON.parse(req.project.columns_config);
  const col = column_name || columns[0];
  if (!columns.includes(col)) return res.status(400).json({ error: `Invalid column. Valid columns: ${columns.join(', ')}` });

  const maxPos = db.prepare('SELECT MAX(position) as maxp FROM cards WHERE project_id = ? AND column_name = ?').get(req.params.projectId, col);
  const position = (maxPos?.maxp ?? -1) + 1;

  const id = uuidv4();
  db.prepare(`INSERT INTO cards (id, project_id, title, description, card_type, column_name, position, priority, assignee_id, labels, created_by)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`).run(
    id, req.params.projectId, title, description || '', card_type || 'feature', col, position, priority || 'medium', assignee_id || null, JSON.stringify(labels || []), req.user.id
  );

  const card = db.prepare('SELECT * FROM cards WHERE id = ?').get(id);
  res.json({ ...card, labels: JSON.parse(card.labels) });
});

app.patch('/api/projects/:projectId/cards/:cardId', authenticate, requireProjectAccess, (req, res) => {
  const card = db.prepare('SELECT * FROM cards WHERE id = ? AND project_id = ?').get(req.params.cardId, req.params.projectId);
  if (!card) return res.status(404).json({ error: 'Card not found' });

  const allowedFields = ['title', 'description', 'card_type', 'column_name', 'position', 'priority', 'assignee_id', 'testing_status', 'testing_result', 'demo_gif_url', 'labels'];
  const updates = [];
  const values = [];

  for (const field of allowedFields) {
    if (req.body[field] !== undefined) {
      updates.push(`${field} = ?`);
      values.push(field === 'labels' ? JSON.stringify(req.body[field]) : req.body[field]);
    }
  }

  if (updates.length === 0) return res.status(400).json({ error: 'No fields to update' });

  updates.push("updated_at = datetime('now')");
  values.push(req.params.cardId, req.params.projectId);

  db.prepare(`UPDATE cards SET ${updates.join(', ')} WHERE id = ? AND project_id = ?`).run(...values);

  const updated = db.prepare(`
    SELECT c.*, u.username as assignee_username, u.display_name as assignee_display_name
    FROM cards c LEFT JOIN users u ON c.assignee_id = u.id WHERE c.id = ?
  `).get(req.params.cardId);
  res.json({ ...updated, labels: JSON.parse(updated.labels) });
});

app.delete('/api/projects/:projectId/cards/:cardId', authenticate, requireProjectAccess, (req, res) => {
  db.prepare('DELETE FROM cards WHERE id = ? AND project_id = ?').run(req.params.cardId, req.params.projectId);
  res.json({ ok: true });
});

// ── Card move (drag & drop) ────────────────────────────────────────────

app.post('/api/projects/:projectId/cards/:cardId/move', authenticate, requireProjectAccess, (req, res) => {
  const { column_name, position } = req.body;
  const card = db.prepare('SELECT * FROM cards WHERE id = ? AND project_id = ?').get(req.params.cardId, req.params.projectId);
  if (!card) return res.status(404).json({ error: 'Card not found' });

  const columns = JSON.parse(req.project.columns_config);
  if (!columns.includes(column_name)) return res.status(400).json({ error: 'Invalid column' });

  const moveCards = db.transaction(() => {
    // Shift cards in target column
    db.prepare('UPDATE cards SET position = position + 1 WHERE project_id = ? AND column_name = ? AND position >= ?').run(req.params.projectId, column_name, position);
    // Move the card
    db.prepare("UPDATE cards SET column_name = ?, position = ?, updated_at = datetime('now') WHERE id = ?").run(column_name, position, req.params.cardId);
  });
  moveCards();

  const updated = db.prepare('SELECT * FROM cards WHERE id = ?').get(req.params.cardId);
  res.json({ ...updated, labels: JSON.parse(updated.labels) });
});

// ── Upload demo GIF ─────────────────────────────────────────────────────

app.post('/api/projects/:projectId/cards/:cardId/upload', authenticate, requireProjectAccess, upload.single('file'), (req, res) => {
  if (!req.file) return res.status(400).json({ error: 'No file uploaded' });

  const url = `/uploads/${req.file.filename}`;
  db.prepare("UPDATE cards SET demo_gif_url = ?, updated_at = datetime('now') WHERE id = ? AND project_id = ?").run(url, req.params.cardId, req.params.projectId);

  res.json({ url });
});

// ── Card Comments ───────────────────────────────────────────────────────

app.get('/api/projects/:projectId/cards/:cardId/comments', authenticate, requireProjectAccess, (req, res) => {
  const comments = db.prepare(`
    SELECT cc.*, u.username, u.display_name FROM card_comments cc
    LEFT JOIN users u ON cc.user_id = u.id
    WHERE cc.card_id = ? ORDER BY cc.created_at ASC
  `).all(req.params.cardId);
  res.json(comments);
});

app.post('/api/projects/:projectId/cards/:cardId/comments', authenticate, requireProjectAccess, (req, res) => {
  const { content, comment_type } = req.body;
  if (!content) return res.status(400).json({ error: 'Content required' });

  const id = uuidv4();
  db.prepare('INSERT INTO card_comments (id, card_id, user_id, content, comment_type) VALUES (?, ?, ?, ?, ?)').run(
    id, req.params.cardId, req.user.id, content, comment_type || 'comment'
  );

  res.json({ id, card_id: req.params.cardId, user_id: req.user.id, content, comment_type: comment_type || 'comment' });
});

// ── Start Server ────────────────────────────────────────────────────────

app.listen(PORT, () => {
  console.log(`Duckllo running on http://localhost:${PORT}`);
});
