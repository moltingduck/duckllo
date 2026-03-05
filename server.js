const express = require('express');
const { Pool } = require('pg');
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

const pool = new Pool({
  host: process.env.DB_HOST || 'localhost',
  port: parseInt(process.env.DB_PORT) || 5432,
  database: process.env.DB_NAME || 'duckllo',
  user: process.env.DB_USER || 'duckllo',
  password: process.env.DB_PASSWORD || 'duckllo',
});

async function initDB() {
  await pool.query(`
    CREATE TABLE IF NOT EXISTS users (
      id VARCHAR(36) PRIMARY KEY,
      username VARCHAR(255) UNIQUE NOT NULL,
      password_hash TEXT NOT NULL,
      display_name VARCHAR(255),
      created_at TIMESTAMP DEFAULT NOW()
    );

    CREATE TABLE IF NOT EXISTS projects (
      id VARCHAR(36) PRIMARY KEY,
      name VARCHAR(255) NOT NULL,
      description TEXT,
      owner_id VARCHAR(36) NOT NULL REFERENCES users(id),
      columns_config JSONB DEFAULT '["Backlog","Todo","In Progress","Review","Done"]',
      created_at TIMESTAMP DEFAULT NOW()
    );

    CREATE TABLE IF NOT EXISTS project_members (
      project_id VARCHAR(36) NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
      user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      role VARCHAR(50) NOT NULL DEFAULT 'member',
      PRIMARY KEY (project_id, user_id)
    );

    CREATE TABLE IF NOT EXISTS api_keys (
      id VARCHAR(36) PRIMARY KEY,
      key_hash TEXT NOT NULL,
      key_prefix VARCHAR(50) NOT NULL,
      label VARCHAR(255),
      user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      project_id VARCHAR(36) NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
      permissions JSONB DEFAULT '["read","write"]',
      created_at TIMESTAMP DEFAULT NOW()
    );

    CREATE TABLE IF NOT EXISTS cards (
      id VARCHAR(36) PRIMARY KEY,
      project_id VARCHAR(36) NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
      title VARCHAR(500) NOT NULL,
      description TEXT,
      card_type VARCHAR(50) NOT NULL DEFAULT 'feature',
      column_name VARCHAR(100) NOT NULL DEFAULT 'Backlog',
      position INTEGER NOT NULL DEFAULT 0,
      priority VARCHAR(50) DEFAULT 'medium',
      assignee_id VARCHAR(36) REFERENCES users(id),
      testing_status VARCHAR(50) DEFAULT 'untested',
      testing_result TEXT,
      demo_gif_url TEXT,
      labels JSONB DEFAULT '[]',
      created_by VARCHAR(36) REFERENCES users(id),
      created_at TIMESTAMP DEFAULT NOW(),
      updated_at TIMESTAMP DEFAULT NOW()
    );

    CREATE TABLE IF NOT EXISTS card_comments (
      id VARCHAR(36) PRIMARY KEY,
      card_id VARCHAR(36) NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
      user_id VARCHAR(36) REFERENCES users(id),
      content TEXT NOT NULL,
      comment_type VARCHAR(50) DEFAULT 'comment',
      created_at TIMESTAMP DEFAULT NOW()
    );

    CREATE TABLE IF NOT EXISTS sessions (
      token VARCHAR(36) PRIMARY KEY,
      user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      created_at TIMESTAMP DEFAULT NOW(),
      expires_at TIMESTAMP NOT NULL
    );

    CREATE TABLE IF NOT EXISTS recovery_codes (
      id VARCHAR(36) PRIMARY KEY,
      user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      code_hash TEXT NOT NULL,
      expires_at TIMESTAMP NOT NULL,
      used BOOLEAN DEFAULT FALSE,
      created_at TIMESTAMP DEFAULT NOW()
    );
  `);
}

// ── Quality Gate Rules ──────────────────────────────────────────────────
// Tags that require demo media/GIF to move to Review/Done
const DEMO_REQUIRED_TAGS = ['ui', 'ux', 'ui/ux', 'frontend', 'user-operation', 'user-facing', 'demo-required'];

function validateCardForGatedColumn(card, targetColumn) {
  if (targetColumn !== 'Review' && targetColumn !== 'Done') return null;

  const labels = (card.labels || []).map(l => l.toLowerCase());
  const needsDemo = labels.some(l => DEMO_REQUIRED_TAGS.includes(l));
  const hasDemo = !!card.demo_gif_url;
  const hasTestResult = !!card.testing_result && card.testing_result.trim().length > 0;

  if (needsDemo && !hasDemo) {
    return `Cards tagged with UI/UX/user-operation labels require a demo GIF/media to move to ${targetColumn}. Missing: demo media. (tags: ${labels.filter(l => DEMO_REQUIRED_TAGS.includes(l)).join(', ')})`;
  }

  if (!hasDemo && !hasTestResult) {
    return `Cards must have at least a test result or demo media to move to ${targetColumn}. Missing: both testing_result and demo_gif_url.`;
  }

  return null; // passes
}

// ── Auth Middleware ──────────────────────────────────────────────────────

async function authenticate(req, res, next) {
  const authHeader = req.headers.authorization;
  if (authHeader) {
    const token = authHeader.replace('Bearer ', '');

    if (token.startsWith('duckllo_')) {
      // API key auth
      const { rows: keys } = await pool.query('SELECT ak.*, u.id as uid, u.username FROM api_keys ak JOIN users u ON ak.user_id = u.id');
      for (const key of keys) {
        if (bcrypt.compareSync(token, key.key_hash)) {
          req.user = { id: key.uid, username: key.username };
          req.apiKeyProjectId = key.project_id;
          req.apiKeyPermissions = key.permissions;
          return next();
        }
      }
      return res.status(401).json({ error: 'Invalid API key' });
    }

    // Session token auth
    const { rows } = await pool.query(
      "SELECT s.*, u.id as uid, u.username, u.display_name FROM sessions s JOIN users u ON s.user_id = u.id WHERE s.token = $1 AND s.expires_at > NOW()",
      [token]
    );
    if (rows[0]) {
      const session = rows[0];
      req.user = { id: session.uid, username: session.username, display_name: session.display_name };
      return next();
    }
  }

  return res.status(401).json({ error: 'Authentication required' });
}

async function requireProjectAccess(req, res, next) {
  const projectId = req.params.projectId || req.body.project_id;
  if (!projectId) return res.status(400).json({ error: 'Project ID required' });

  if (req.apiKeyProjectId && req.apiKeyProjectId !== projectId) {
    return res.status(403).json({ error: 'API key not authorized for this project' });
  }

  const { rows: projects } = await pool.query('SELECT * FROM projects WHERE id = $1', [projectId]);
  if (!projects[0]) return res.status(404).json({ error: 'Project not found' });
  const project = projects[0];

  const { rows: members } = await pool.query('SELECT * FROM project_members WHERE project_id = $1 AND user_id = $2', [projectId, req.user.id]);
  if (!members[0] && project.owner_id !== req.user.id) {
    return res.status(403).json({ error: 'Not a member of this project' });
  }

  req.project = project;
  req.memberRole = members[0] ? members[0].role : (project.owner_id === req.user.id ? 'owner' : null);
  next();
}

// ── Auth Routes ─────────────────────────────────────────────────────────

app.post('/api/auth/register', async (req, res) => {
  try {
    const { username, password, display_name } = req.body;
    if (!username || !password) return res.status(400).json({ error: 'Username and password required' });
    if (username.length < 3) return res.status(400).json({ error: 'Username must be at least 3 characters' });
    if (password.length < 6) return res.status(400).json({ error: 'Password must be at least 6 characters' });

    const { rows: existing } = await pool.query('SELECT id FROM users WHERE username = $1', [username]);
    if (existing[0]) return res.status(409).json({ error: 'Username already exists' });

    const id = uuidv4();
    const password_hash = bcrypt.hashSync(password, 10);
    await pool.query('INSERT INTO users (id, username, password_hash, display_name) VALUES ($1, $2, $3, $4)', [id, username, password_hash, display_name || username]);

    const token = uuidv4();
    const expires = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString();
    await pool.query('INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, $3)', [token, id, expires]);

    res.json({ token, user: { id, username, display_name: display_name || username } });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/auth/login', async (req, res) => {
  try {
    const { username, password } = req.body;
    const { rows } = await pool.query('SELECT * FROM users WHERE username = $1', [username]);
    if (!rows[0] || !bcrypt.compareSync(password, rows[0].password_hash)) {
      return res.status(401).json({ error: 'Invalid credentials' });
    }
    const user = rows[0];
    const token = uuidv4();
    const expires = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString();
    await pool.query('INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, $3)', [token, user.id, expires]);

    res.json({ token, user: { id: user.id, username: user.username, display_name: user.display_name } });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.get('/api/auth/me', authenticate, (req, res) => {
  res.json({ user: req.user });
});

app.post('/api/auth/logout', authenticate, async (req, res) => {
  const token = req.headers.authorization?.replace('Bearer ', '');
  await pool.query('DELETE FROM sessions WHERE token = $1', [token]);
  res.json({ ok: true });
});

app.post('/api/auth/reset-password', async (req, res) => {
  try {
    const { username, recovery_code, new_password } = req.body;
    if (!username || !recovery_code || !new_password) {
      return res.status(400).json({ error: 'username, recovery_code, and new_password required' });
    }
    if (new_password.length < 6) return res.status(400).json({ error: 'Password must be at least 6 characters' });

    const { rows: users } = await pool.query('SELECT id FROM users WHERE username = $1', [username]);
    if (!users[0]) return res.status(404).json({ error: 'User not found' });
    const userId = users[0].id;

    const { rows: codes } = await pool.query(
      'SELECT * FROM recovery_codes WHERE user_id = $1 AND used = FALSE AND expires_at > NOW() ORDER BY created_at DESC',
      [userId]
    );

    let matched = null;
    for (const code of codes) {
      if (bcrypt.compareSync(recovery_code, code.code_hash)) {
        matched = code;
        break;
      }
    }
    if (!matched) return res.status(401).json({ error: 'Invalid or expired recovery code' });

    const password_hash = bcrypt.hashSync(new_password, 10);
    await pool.query('UPDATE users SET password_hash = $1 WHERE id = $2', [password_hash, userId]);
    await pool.query('UPDATE recovery_codes SET used = TRUE WHERE id = $1', [matched.id]);
    // Invalidate all existing sessions
    await pool.query('DELETE FROM sessions WHERE user_id = $1', [userId]);

    res.json({ ok: true, message: 'Password reset successfully. All sessions invalidated.' });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Project Routes ──────────────────────────────────────────────────────

app.get('/api/projects', authenticate, async (req, res) => {
  try {
    const { rows } = await pool.query(`
      SELECT DISTINCT p.* FROM projects p
      LEFT JOIN project_members pm ON p.id = pm.project_id
      WHERE p.owner_id = $1 OR pm.user_id = $1
      ORDER BY p.created_at DESC
    `, [req.user.id]);
    res.json(rows);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/projects', authenticate, async (req, res) => {
  try {
    const { name, description, columns } = req.body;
    if (!name) return res.status(400).json({ error: 'Project name required' });

    const id = uuidv4();
    const columns_config = JSON.stringify(columns || ['Backlog', 'Todo', 'In Progress', 'Review', 'Done']);
    await pool.query('INSERT INTO projects (id, name, description, owner_id, columns_config) VALUES ($1, $2, $3, $4, $5)', [id, name, description || '', req.user.id, columns_config]);
    await pool.query('INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)', [id, req.user.id, 'owner']);

    // Pin quality gate rules card
    const rulesCardId = uuidv4();
    const rulesDesc = [
      '## Card Quality Gate Rules (enforced by server)',
      '',
      'These rules are enforced automatically when moving cards to **Review** or **Done**.',
      '',
      '### All cards must have at least ONE of:',
      '- A **test result** (`testing_result` field with actual output)',
      '- A **demo GIF/media** (uploaded via the card)',
      '',
      '### Cards tagged with UI/UX/user-facing labels MUST have:',
      '- A **demo GIF/media** showing the feature in action',
      '- Tags that trigger this rule: `ui`, `ux`, `ui/ux`, `frontend`, `user-operation`, `user-facing`, `demo-required`',
      '',
      '### Bug fix / performance cards need:',
      '- A **test result** proving the fix works',
      '',
      '### How it works:',
      '- Server rejects moves to Review/Done if requirements are not met (HTTP 422)',
      '- This applies to both drag-and-drop moves and API updates',
      '- Add the appropriate labels when creating cards so the correct rules apply',
      '',
      '*This card is auto-created for every project. Do not delete it.*',
    ].join('\n');
    await pool.query(
      `INSERT INTO cards (id, project_id, title, description, card_type, column_name, position, priority, labels, testing_status, testing_result, created_by)
       VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
      [rulesCardId, id, 'Quality Gate Rules (pinned)', rulesDesc, 'task', 'Backlog', 0, 'critical',
       JSON.stringify(['rules', 'pinned']), 'passed', 'Enforced by server — no test needed.', req.user.id]
    );

    res.json({ id, name, description, owner_id: req.user.id, columns_config: JSON.parse(columns_config) });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.get('/api/projects/:projectId', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows: members } = await pool.query(`
      SELECT u.id, u.username, u.display_name, pm.role FROM project_members pm
      JOIN users u ON pm.user_id = u.id WHERE pm.project_id = $1
    `, [req.params.projectId]);
    res.json({ ...req.project, members });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/projects/:projectId/members', authenticate, requireProjectAccess, async (req, res) => {
  try {
    if (req.memberRole !== 'owner') return res.status(403).json({ error: 'Only owner can add members' });

    const { username, role } = req.body;
    const { rows: users } = await pool.query('SELECT id FROM users WHERE username = $1', [username]);
    if (!users[0]) return res.status(404).json({ error: 'User not found' });

    const { rows: existing } = await pool.query('SELECT * FROM project_members WHERE project_id = $1 AND user_id = $2', [req.params.projectId, users[0].id]);
    if (existing[0]) return res.status(409).json({ error: 'User is already a member' });

    await pool.query('INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)', [req.params.projectId, users[0].id, role || 'member']);
    res.json({ ok: true });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── API Key Routes ──────────────────────────────────────────────────────

app.get('/api/projects/:projectId/api-keys', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows } = await pool.query('SELECT id, key_prefix, label, permissions, created_at FROM api_keys WHERE project_id = $1 AND user_id = $2', [req.params.projectId, req.user.id]);
    res.json(rows);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/projects/:projectId/api-keys', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { label, permissions } = req.body;
    const rawKey = `duckllo_${uuidv4().replace(/-/g, '')}`;
    const id = uuidv4();
    const key_hash = bcrypt.hashSync(rawKey, 10);
    const key_prefix = rawKey.substring(0, 15) + '...';

    await pool.query('INSERT INTO api_keys (id, key_hash, key_prefix, label, user_id, project_id, permissions) VALUES ($1, $2, $3, $4, $5, $6, $7)', [
      id, key_hash, key_prefix, label || 'Agent Key', req.user.id, req.params.projectId, JSON.stringify(permissions || ['read', 'write'])
    ]);

    res.json({ id, key: rawKey, key_prefix, label: label || 'Agent Key' });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.delete('/api/projects/:projectId/api-keys/:keyId', authenticate, requireProjectAccess, async (req, res) => {
  await pool.query('DELETE FROM api_keys WHERE id = $1 AND project_id = $2 AND user_id = $3', [req.params.keyId, req.params.projectId, req.user.id]);
  res.json({ ok: true });
});

// ── Card Routes ─────────────────────────────────────────────────────────

app.get('/api/projects/:projectId/cards', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows } = await pool.query(`
      SELECT c.*, u.username as assignee_username, u.display_name as assignee_display_name,
             cr.username as creator_username
      FROM cards c
      LEFT JOIN users u ON c.assignee_id = u.id
      LEFT JOIN users cr ON c.created_by = cr.id
      WHERE c.project_id = $1
      ORDER BY c.position ASC
    `, [req.params.projectId]);
    res.json(rows);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/projects/:projectId/cards', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { title, description, card_type, column_name, priority, assignee_id, labels } = req.body;
    if (!title) return res.status(400).json({ error: 'Title required' });

    const columns = req.project.columns_config;
    const col = column_name || columns[0];
    if (!columns.includes(col)) return res.status(400).json({ error: `Invalid column. Valid columns: ${columns.join(', ')}` });

    const { rows: maxRows } = await pool.query('SELECT MAX(position) as maxp FROM cards WHERE project_id = $1 AND column_name = $2', [req.params.projectId, col]);
    const position = (maxRows[0]?.maxp ?? -1) + 1;

    const id = uuidv4();
    await pool.query(`INSERT INTO cards (id, project_id, title, description, card_type, column_name, position, priority, assignee_id, labels, created_by)
      VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, [
      id, req.params.projectId, title, description || '', card_type || 'feature', col, position, priority || 'medium', assignee_id || null, JSON.stringify(labels || []), req.user.id
    ]);

    const { rows } = await pool.query('SELECT * FROM cards WHERE id = $1', [id]);
    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.patch('/api/projects/:projectId/cards/:cardId', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows: cards } = await pool.query('SELECT * FROM cards WHERE id = $1 AND project_id = $2', [req.params.cardId, req.params.projectId]);
    if (!cards[0]) return res.status(404).json({ error: 'Card not found' });

    // Quality gate: check if column_name is changing to a gated column
    if (req.body.column_name) {
      // Merge current card with incoming changes to check against final state
      const merged = { ...cards[0] };
      if (req.body.labels !== undefined) merged.labels = req.body.labels;
      if (req.body.testing_result !== undefined) merged.testing_result = req.body.testing_result;
      if (req.body.demo_gif_url !== undefined) merged.demo_gif_url = req.body.demo_gif_url;
      const gateError = validateCardForGatedColumn(merged, req.body.column_name);
      if (gateError) return res.status(422).json({ error: gateError });
    }

    const allowedFields = ['title', 'description', 'card_type', 'column_name', 'position', 'priority', 'assignee_id', 'testing_status', 'testing_result', 'demo_gif_url', 'labels'];
    const updates = [];
    const values = [];
    let paramIdx = 1;

    for (const field of allowedFields) {
      if (req.body[field] !== undefined) {
        updates.push(`${field} = $${paramIdx++}`);
        values.push(field === 'labels' ? JSON.stringify(req.body[field]) : req.body[field]);
      }
    }

    if (updates.length === 0) return res.status(400).json({ error: 'No fields to update' });

    updates.push(`updated_at = NOW()`);
    values.push(req.params.cardId, req.params.projectId);

    await pool.query(`UPDATE cards SET ${updates.join(', ')} WHERE id = $${paramIdx++} AND project_id = $${paramIdx}`, values);

    const { rows } = await pool.query(`
      SELECT c.*, u.username as assignee_username, u.display_name as assignee_display_name
      FROM cards c LEFT JOIN users u ON c.assignee_id = u.id WHERE c.id = $1
    `, [req.params.cardId]);
    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.delete('/api/projects/:projectId/cards/:cardId', authenticate, requireProjectAccess, async (req, res) => {
  await pool.query('DELETE FROM cards WHERE id = $1 AND project_id = $2', [req.params.cardId, req.params.projectId]);
  res.json({ ok: true });
});

// ── Card move (drag & drop) ────────────────────────────────────────────

app.post('/api/projects/:projectId/cards/:cardId/move', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { column_name, position } = req.body;
    const { rows: cards } = await pool.query('SELECT * FROM cards WHERE id = $1 AND project_id = $2', [req.params.cardId, req.params.projectId]);
    if (!cards[0]) return res.status(404).json({ error: 'Card not found' });

    const columns = req.project.columns_config;
    if (!columns.includes(column_name)) return res.status(400).json({ error: 'Invalid column' });

    // Quality gate check
    const gateError = validateCardForGatedColumn(cards[0], column_name);
    if (gateError) return res.status(422).json({ error: gateError });

    const client = await pool.connect();
    try {
      await client.query('BEGIN');
      await client.query('UPDATE cards SET position = position + 1 WHERE project_id = $1 AND column_name = $2 AND position >= $3', [req.params.projectId, column_name, position]);
      await client.query('UPDATE cards SET column_name = $1, position = $2, updated_at = NOW() WHERE id = $3', [column_name, position, req.params.cardId]);
      await client.query('COMMIT');
    } catch (e) {
      await client.query('ROLLBACK');
      throw e;
    } finally {
      client.release();
    }

    const { rows } = await pool.query('SELECT * FROM cards WHERE id = $1', [req.params.cardId]);
    res.json(rows[0]);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Upload demo GIF ─────────────────────────────────────────────────────

app.post('/api/projects/:projectId/cards/:cardId/upload', authenticate, requireProjectAccess, upload.single('file'), async (req, res) => {
  if (!req.file) return res.status(400).json({ error: 'No file uploaded' });

  const url = `/uploads/${req.file.filename}`;
  await pool.query('UPDATE cards SET demo_gif_url = $1, updated_at = NOW() WHERE id = $2 AND project_id = $3', [url, req.params.cardId, req.params.projectId]);

  res.json({ url });
});

// ── Card Comments ───────────────────────────────────────────────────────

app.get('/api/projects/:projectId/cards/:cardId/comments', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { rows } = await pool.query(`
      SELECT cc.*, u.username, u.display_name FROM card_comments cc
      LEFT JOIN users u ON cc.user_id = u.id
      WHERE cc.card_id = $1 ORDER BY cc.created_at ASC
    `, [req.params.cardId]);
    res.json(rows);
  } catch (err) { res.status(500).json({ error: err.message }); }
});

app.post('/api/projects/:projectId/cards/:cardId/comments', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const { content, comment_type } = req.body;
    if (!content) return res.status(400).json({ error: 'Content required' });

    const id = uuidv4();
    await pool.query('INSERT INTO card_comments (id, card_id, user_id, content, comment_type) VALUES ($1, $2, $3, $4, $5)', [
      id, req.params.cardId, req.user.id, content, comment_type || 'comment'
    ]);

    res.json({ id, card_id: req.params.cardId, user_id: req.user.id, content, comment_type: comment_type || 'comment' });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Activity Feed ───────────────────────────────────────────────────────

app.get('/api/projects/:projectId/activity', authenticate, requireProjectAccess, async (req, res) => {
  try {
    const since = req.query.since || new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString();
    const limit = Math.min(parseInt(req.query.limit) || 50, 200);

    const { rows: updatedCards } = await pool.query(`
      SELECT c.id, c.title, c.card_type, c.column_name, c.priority, c.testing_status,
             c.updated_at, c.created_at, cr.username as creator_username,
             'card_updated' as event_type
      FROM cards c
      LEFT JOIN users cr ON c.created_by = cr.id
      WHERE c.project_id = $1 AND c.updated_at > $2
      ORDER BY c.updated_at DESC
      LIMIT $3
    `, [req.params.projectId, since, limit]);

    const { rows: newComments } = await pool.query(`
      SELECT cc.id, cc.card_id, cc.content, cc.comment_type, cc.created_at,
             u.username, u.display_name,
             c.title as card_title,
             'comment_added' as event_type
      FROM card_comments cc
      JOIN cards c ON cc.card_id = c.id
      LEFT JOIN users u ON cc.user_id = u.id
      WHERE c.project_id = $1 AND cc.created_at > $2
      ORDER BY cc.created_at DESC
      LIMIT $3
    `, [req.params.projectId, since, limit]);

    const events = [
      ...updatedCards.map(c => ({ ...c, timestamp: c.updated_at })),
      ...newComments.map(c => ({ ...c, timestamp: c.created_at }))
    ].sort((a, b) => new Date(b.timestamp) - new Date(a.timestamp)).slice(0, limit);

    res.json({ since, count: events.length, events });
  } catch (err) { res.status(500).json({ error: err.message }); }
});

// ── Start Server ────────────────────────────────────────────────────────

async function generateRecoveryCode(username) {
  await initDB();
  const { rows } = await pool.query('SELECT id FROM users WHERE username = $1', [username]);
  if (!rows[0]) {
    console.error(`Error: User "${username}" not found.`);
    process.exit(1);
  }

  const crypto = require('crypto');
  const code = crypto.randomBytes(16).toString('hex');
  const code_hash = bcrypt.hashSync(code, 10);
  const id = uuidv4();
  const expires_at = new Date(Date.now() + 60 * 60 * 1000).toISOString(); // 1 hour

  await pool.query(
    'INSERT INTO recovery_codes (id, user_id, code_hash, expires_at) VALUES ($1, $2, $3, $4)',
    [id, rows[0].id, code_hash, expires_at]
  );

  console.log(`Recovery code for "${username}":`);
  console.log(`  Code: ${code}`);
  console.log(`  Expires: ${new Date(expires_at).toLocaleString()}`);
  console.log(`\nUser resets password with:`);
  console.log(`  POST /api/auth/reset-password`);
  console.log(`  {"username":"${username}","recovery_code":"${code}","new_password":"<new>"}`);
  await pool.end();
}

async function start() {
  const args = process.argv.slice(2);
  const recoverIdx = args.indexOf('--recover');
  if (recoverIdx !== -1) {
    const username = args[recoverIdx + 1];
    if (!username) {
      console.error('Usage: node server.js --recover <username>');
      process.exit(1);
    }
    return generateRecoveryCode(username);
  }

  await initDB();
  app.listen(PORT, () => {
    console.log(`Duckllo running on http://localhost:${PORT}`);
  });
}

start().catch(err => {
  console.error('Failed to start:', err.message);
  process.exit(1);
});
