import { translator } from './i18n.js';
import { paths, requestParams, formatValue } from './data.mjs';

const sdk = window.AIHubPluginSDK;
const React = sdk.React;
const { useState, useEffect, useCallback, useMemo, useRef } = React;
const { createFilterOptions, Alert, Autocomplete, Box, Button, Chip, FormControlLabel, MenuItem, Paper, Skeleton, Stack, Switch, Tab, Tabs, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, TextField, Typography, useTheme } = sdk.MUI;

async function request(path, config = {}) {
  const bases = sdk.getPluginBases('monitor');
  if (!bases?.apiBase) throw new Error('Plugin bases unavailable');
  const response = await sdk.API({ method: 'get', url: `${bases.apiBase}${path}`, suppressGlobalError: true, ...config });
  if (!response.data?.success) throw new Error(response.data?.message || 'Request failed');
  return response.data.data;
}

const filterChoices = createFilterOptions({ matchFrom: 'any', ignoreCase: true, trim: true });

export function navigation() {
  const t = translator(sdk.i18n.language);
  return [{ id: 'monitor', title: t('title'), url: '/panel/plugins/monitor', type: 'item', breadcrumbs: false, icon: 'tabler:activity' }];
}

const dateInput = (at) => { const d = new Date(at); return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16); };
const initialFilters = () => ({ scope: 'gateway', range: 3600, live: true, start: dateInput(Date.now() - 3600000), end: dateInput(Date.now()), model_name: '', channel_id: '', instance_id: '', api_mode: '', stream: '', stage: '' });

export function Page({ path = '' }) {
  const { i18n } = sdk.useTranslation();
  const t = translator(i18n.language);
  const [access, setAccess] = useState(null);
  const [error, setError] = useState('');
  const [tab, setTab] = useState(['gateway', 'model', 'billing', 'status', 'settings'].includes(path) ? path : 'gateway');
  useEffect(() => {
    const abort = new AbortController();
    request('/self', { signal: abort.signal }).then(setAccess).catch((e) => { if (!abort.signal.aborted) setError(e.message); });
    return () => abort.abort();
  }, []);
  if (error) return <Alert severity="error">{t('failed')}: {error}</Alert>;
  if (!access) return <Stack spacing={2}><Skeleton height={40} /><Skeleton height={300} /></Stack>;
  if (!access.can_view) return <Alert severity="info">{t('denied')}</Alert>;
  return <Box sx={{ width: '100%', minWidth: 0, color: 'text.primary' }}>
    <Typography variant="h4" sx={{ fontWeight: 600 }}>{t('title')}</Typography>
    <Typography color="text.secondary" variant="body2" sx={{ mt: 0.5, mb: 2 }}>{t('intro')}</Typography>
    <Tabs value={tab === 'settings' && !access.can_configure ? 'gateway' : tab} onChange={(_, next) => setTab(next)} variant="scrollable" scrollButtons="auto" sx={{ borderBottom: 1, borderColor: 'divider', mb: 2 }}>
      {['gateway', 'model', 'billing', 'status', ...(access.can_configure ? ['settings'] : [])].map((key) => <Tab key={key} value={key} label={t(`${key}_tab`)} />)}
    </Tabs>
    {tab === 'settings' && access.can_configure ? <Settings t={t} /> : tab === 'status' ? <Status t={t} /> : <Dashboard key={tab} scope={tab === 'settings' ? 'gateway' : tab} t={t} language={i18n.language.replaceAll('_', '-')} canConfigure={access.can_configure} onConfigure={() => setTab('settings')} />}
  </Box>;
}

function SearchFilter({ value, values = [], onChange, placeholder }) {
  return <Autocomplete size="small" forcePopupIcon={false} openOnFocus autoHighlight filterOptions={filterChoices} options={values} value={value || null} onChange={(_, next) => onChange(next || '')} sx={{ minWidth: 150, flex: '1 1 150px', maxWidth: 280 }} renderInput={(params) => <TextField {...params} placeholder={placeholder} inputProps={{ ...params.inputProps, 'aria-label': placeholder }} />} />;
}

function Dashboard({ scope, t, language, canConfigure, onConfigure }) {
  const [filters, setFilters] = useState(() => ({ ...initialFilters(), scope }));
  const [data, setData] = useState(null);
  const [options, setOptions] = useState({});
  const [status, setStatus] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const active = useRef(null);
  const update = (key, value) => setFilters((old) => ({ ...old, [key]: value }));
  const load = useCallback(async () => {
    if (document.hidden) return;
    active.current?.abort();
    const abort = new AbortController(); active.current = abort;
    setLoading(true); setError('');
    try {
      const params = requestParams(filters);
      const [next, choices, health] = await Promise.all([request('/series', { params, signal: abort.signal }), request('/options', { params, signal: abort.signal }), request('/status', { signal: abort.signal })]);
      if (abort.signal.aborted) return;
      setData(next); setOptions(choices); setStatus(health);
    } catch (e) { if (!abort.signal.aborted) setError(e.message === 'invalid_range' ? t('invalid_range') : e.response?.data?.message || e.message); }
    finally { if (!abort.signal.aborted) setLoading(false); }
  }, [filters, t]);
  // t is intentionally stable through the language key supplied by the parent.
  const latestLoad = useRef(load); latestLoad.current = load;
  const filtersKey = JSON.stringify(filters);
  useEffect(() => {
    setData(null); latestLoad.current();
    const timer = filters.live ? setInterval(() => { if (!document.hidden) latestLoad.current(); }, 15000) : null;
    const visibility = () => { if (document.hidden) active.current?.abort(); else latestLoad.current(); };
    document.addEventListener('visibilitychange', visibility);
    return () => { clearInterval(timer); active.current?.abort(); document.removeEventListener('visibilitychange', visibility); };
  }, [filtersKey, filters.live, language]);
  const histograms = data?.metrics.filter((m) => m.histogram) || [];
  const scalars = data?.metrics.filter((m) => !m.histogram) || [];
  return <Stack spacing={2}>
    <Paper variant="outlined" sx={{ p: 1.5, borderRadius: 1, borderColor: 'divider', '& .MuiOutlinedInput-root': { height: 36 }, '& .MuiOutlinedInput-notchedOutline': { borderColor: 'divider' } }}>
      <Stack direction="row" spacing={1} useFlexGap flexWrap="wrap" alignItems="center">
        <TextField select size="small" value={filters.range} onChange={(e) => update('range', Number(e.target.value))} inputProps={{ 'aria-label': t('range') }} sx={{ minWidth: 150 }}>
          {[[3600, 'one_hour'], [21600, 'six_hours'], [86400, 'one_day'], [604800, 'seven_days'], [2592000, 'thirty_days']].map(([value, key]) => <MenuItem key={value} value={value}>{t(key)}</MenuItem>)}
        </TextField>
        <SearchFilter value={filters.instance_id} values={options.gateway_instance} onChange={(v) => update('instance_id', v)} placeholder={t('instance')} />
        {scope !== 'billing' && <>
          <SearchFilter value={filters.model_name} values={options.model} onChange={(v) => update('model_name', v)} placeholder={t('model')} />
          <SearchFilter value={filters.channel_id} values={options.channel_id} onChange={(v) => update('channel_id', v)} placeholder={t('channel')} />
          <SearchFilter value={filters.api_mode} values={options.api_mode} onChange={(v) => update('api_mode', v)} placeholder={t('api_mode')} />
          <TextField select size="small" value={filters.stream} onChange={(e) => update('stream', e.target.value)} SelectProps={{ displayEmpty: true }} sx={{ minWidth: 130 }} inputProps={{ 'aria-label': t('stream') }}>
            <MenuItem value="">{t('all')} · {t('stream')}</MenuItem><MenuItem value="true">{t('stream')}</MenuItem><MenuItem value="false">{t('non_stream')}</MenuItem>
          </TextField>
        </>}
        {scope === 'billing' && <SearchFilter value={filters.stage} values={options.stage} onChange={(v) => update('stage', v)} placeholder={t('stage')} />}
        <FormControlLabel sx={{ mr: 0 }} control={<Switch size="small" checked={filters.live} onChange={(_, v) => update('live', v)} />} label={t('live')} />
        <Button variant="outlined" size="small" disabled={loading} onClick={load} sx={{ height: 36, minHeight: 36, borderColor: 'divider', color: 'text.primary', '&:hover': { borderColor: 'text.disabled', bgcolor: 'action.hover' } }}>{t('refresh')}</Button>
      </Stack>
      {!filters.live && <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ mt: 1.5 }}>
        {['start', 'end'].map((key) => <TextField key={key} size="small" type="datetime-local" value={filters[key]} onChange={(e) => update(key, e.target.value)} inputProps={{ 'aria-label': t(key) }} />)}
      </Stack>}
    </Paper>
    {error && <Alert severity="error">{t('failed')}: {error}</Alert>}
    {status && !status.configured && <Alert severity="info" action={canConfigure ? <Button color="inherit" onClick={onConfigure}>{t('settings_tab')}</Button> : null}>{t('configure')}</Alert>}
    {status?.configured && !status.complete && <Alert severity="warning">{t('partial')}</Alert>}
    {scope === 'billing' && status && <Stack direction="row" spacing={1} useFlexGap flexWrap="wrap">
      {status.instances.filter((row) => row.worker_enabled).map((row) => <Chip key={row.id} variant="outlined" size="small" color={row.health === 'up' && row.worker_ready ? 'success' : 'warning'} label={`${row.hostname || row.id} · ${t(`state_${row.health}`)} · ${t(row.worker_ready ? 'ready' : 'unavailable')}`} />)}
    </Stack>}
    <Typography variant="caption" color="text.secondary">{scope === 'billing' ? t('billing_note') : t('approximate')} {data && `${t('loaded')}: ${new Date(data.queried_at * 1000).toLocaleTimeString(language)}`}</Typography>
    {!data && loading ? <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', md: '1fr 1fr' }, gap: 2 }}>{[0, 1, 2, 3].map((key) => <Skeleton key={key} variant="rectangular" height={250} />)}</Box> : <>
      <Box sx={{ display: 'grid', gridTemplateColumns: { xs: 'minmax(0,1fr)', lg: 'repeat(2,minmax(0,1fr))' }, gap: 1.5 }}>
        {histograms.map((metric) => <MetricPanel key={metric.key} metric={metric} t={t} language={language} />)}
      </Box>
      {data?.models?.length > 0 && <Paper variant="outlined" sx={{ p: 1.5, minWidth: 0 }}>
        <Typography variant="subtitle2" sx={{ mb: 1 }}>{t('model_comparison')}</Typography>
        <TableContainer><Table size="small"><TableHead><TableRow><TableCell>{t('model')}</TableCell>{['gateway', 'ttft', 'tpot', 'e2e'].map((key) => <TableCell key={key} align="right">{key.toUpperCase()} P95</TableCell>)}</TableRow></TableHead>
          <TableBody>{data.models.map((row) => <TableRow key={row.model} hover sx={{ cursor: 'pointer' }} onClick={() => update('model_name', row.model)}><TableCell><Button variant="text" onClick={() => update('model_name', row.model)} sx={{ textTransform: 'none', p: 0 }}>{row.model}</Button></TableCell>{['gateway', 'ttft', 'tpot', 'e2e'].map((key) => <TableCell key={key} align="right">{row.lower_bounds?.[key]?.p95 ? '≥ ' : ''}{formatValue(row.metrics[key]?.p95, 'ms', language)} {key === 'tpot' ? 'ms/token' : 'ms'}</TableCell>)}</TableRow>)}</TableBody>
        </Table></TableContainer>
      </Paper>}
      <Box sx={{ display: 'grid', gridTemplateColumns: { xs: 'minmax(0,1fr)', sm: 'repeat(2,minmax(0,1fr))', xl: 'repeat(4,minmax(0,1fr))' }, gap: 1.5 }}>
        {scalars.map((metric) => <MetricPanel key={metric.key} metric={metric} t={t} language={language} />)}
      </Box>
    </>}
  </Stack>;
}

function MetricPanel({ metric, t, language }) {
  const primary = metric.histogram ? 'p95' : 'value';
  const summary = metric.summary || {};
  return <Paper component="article" variant="outlined" sx={{ p: 1.5, minWidth: 0, borderRadius: 1 }}>
    <Typography variant="subtitle2">{t(metric.key)}</Typography>
    {!metric.supported ? <Typography color="text.secondary" variant="body2" sx={{ py: 3 }}>{t('unsupported')}</Typography> : <>
      <Typography variant={metric.histogram ? 'h5' : 'h6'} sx={{ mt: 0.5, mb: 0.5, fontVariantNumeric: 'tabular-nums' }}>{metric.lower_bounds?.[primary] ? '≥ ' : ''}{formatValue(summary[primary], metric.unit, language)} <Typography component="span" variant="caption" color="text.secondary">{metric.histogram ? 'P95 · ' : ''}{metric.unit === 'bytes' ? '' : metric.unit}</Typography></Typography>
      {metric.histogram && <Stack direction="row" spacing={1.5} useFlexGap flexWrap="wrap" sx={{ minHeight: 25 }}>
        {['p50', 'p90', 'p95', 'p99'].map((key) => <Typography key={key} variant="caption" color="text.secondary" sx={{ fontVariantNumeric: 'tabular-nums' }}>{key.toUpperCase()} {metric.lower_bounds?.[key] ? '≥ ' : ''}{formatValue(summary[key], metric.unit, language)}</Typography>)}
      </Stack>}
      <Chart metric={metric} t={t} language={language} />
      {metric.stale && <Typography variant="caption" color="warning.main">{t('stale')}: {new Date(summary.observed_at * 1000).toLocaleString(language)}</Typography>}
      {metric.histogram && <Typography color="text.secondary" variant="caption">{t('mean')}: {formatValue(summary.mean, metric.unit, language)} · {t('count')}: {formatValue(summary.count, '', language)}</Typography>}
    </>}
  </Paper>;
}

function Chart({ metric, t, language }) {
  const theme = useTheme();
  const [hover, setHover] = useState(null);
  const stats = useMemo(() => metric.histogram ? ['p50', 'p90', 'p95', 'p99'] : ['value'], [metric.histogram]);
  const geometry = useMemo(() => paths(metric.points, stats), [metric.points, stats]);
  const colors = [theme.palette.primary.main, theme.palette.info.main, theme.palette.warning.main, theme.palette.error.main];
  const hasData = metric.points.some((point) => stats.some((stat) => point.values[stat] != null));
  if (!hasData) return <Box sx={{ height: metric.histogram ? 160 : 100, display: 'grid', placeItems: 'center' }}><Typography color="text.secondary" variant="caption">{t('no_data')}</Typography></Box>;
  const selected = hover == null ? null : metric.points[hover];
  return <Box sx={{ position: 'relative', mt: 1 }}>
    <svg viewBox="0 0 600 155" width="100%" style={{ display: 'block', overflow: 'visible' }} role="img" aria-label={t(metric.key)} onPointerLeave={() => setHover(null)} onPointerMove={(e) => { const rect = e.currentTarget.getBoundingClientRect(); setHover(Math.max(0, Math.min(metric.points.length - 1, Math.round(((e.clientX - rect.left) / rect.width * 600 - geometry.left) / (geometry.right - geometry.left) * (metric.points.length - 1))))); }}>
      {[0, 0.5, 1].map((ratio) => { const y = geometry.bottom - ratio * (geometry.bottom - geometry.top); return <g key={ratio}><line x1={geometry.left} x2={geometry.right} y1={y} y2={y} stroke={theme.palette.divider} strokeDasharray="3 4" /><text x={geometry.left - 5} y={y + 3} textAnchor="end" fill={theme.palette.text.secondary} fontSize="10">{formatValue(geometry.ceiling * ratio, metric.unit, language)}</text></g>; })}
      {geometry.lines.map((line, index) => <path key={line.stat} d={line.d} stroke={colors[index]} strokeWidth="1.8" strokeLinecap="round" vectorEffect="non-scaling-stroke" fill="none" />)}
      {[0, Math.floor((metric.points.length - 1) / 2), metric.points.length - 1].map((index, n) => <text key={n} x={geometry.left + n / 2 * (geometry.right - geometry.left)} y="149" textAnchor={n === 0 ? 'start' : n === 2 ? 'end' : 'middle'} fill={theme.palette.text.secondary} fontSize="10">{new Date(metric.points[index].timestamp * 1000).toLocaleString(language, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })}</text>)}
    </svg>
    {metric.histogram && <Stack direction="row" spacing={1.5}>{stats.map((stat, index) => <Typography key={stat} variant="caption" sx={{ color: colors[index] }}>{stat.toUpperCase()}</Typography>)}</Stack>}
    {selected && <Paper variant="outlined" sx={{ position: 'absolute', top: 0, right: 4, p: 0.75, pointerEvents: 'none', zIndex: 1 }}><Typography variant="caption" component="div">{new Date(selected.timestamp * 1000).toLocaleString(language)}</Typography>{stats.map((stat, index) => <Typography key={stat} variant="caption" component="div" sx={{ color: colors[index] }}>{stat === 'value' ? '' : stat.toUpperCase()} {selected.lower_bounds?.[stat] ? '≥ ' : ''}{formatValue(selected.values[stat], metric.unit, language)}</Typography>)}</Paper>}
  </Box>;
}

function Status({ t }) {
  const [data, setData] = useState(null);
  const [error, setError] = useState('');
  useEffect(() => {
    let active = true;
    let pending = null;
    const load = () => {
      pending?.abort();
      if (document.hidden) return;
      const abort = new AbortController(); pending = abort;
      request('/status', { signal: abort.signal }).then((next) => { if (active && !abort.signal.aborted) { setData(next); setError(''); } }).catch((e) => { if (active && !abort.signal.aborted) setError(e.message); });
    };
    load(); const timer = setInterval(load, 15000);
    document.addEventListener('visibilitychange', load);
    return () => { active = false; pending?.abort(); clearInterval(timer); document.removeEventListener('visibilitychange', load); };
  }, []);
  return <Stack spacing={2}>
    {error && <Alert severity="error">{t('failed')}: {error}</Alert>}
    {!data ? <Skeleton height={200} /> : <>
      <Stack direction="row" spacing={1} alignItems="center"><Typography variant="subtitle2">{t('database')}</Typography><Chip size="small" color={data.database_available ? 'success' : 'error'} variant="outlined" label={t(data.database_available ? 'healthy' : 'unavailable')} /></Stack>
      <Typography variant="caption" color="text.secondary">{t('last_discovery')}: {data.discovery_last_success_at ? new Date(data.discovery_last_success_at * 1000).toLocaleString() : '—'}</Typography>
      {data.discovery_error && <Alert severity="warning">{data.discovery_error}</Alert>}
      <Paper variant="outlined"><TableContainer><Table size="small"><TableHead><TableRow>{['instance', 'role', 'ready', 'state', 'last_scrape'].map((key) => <TableCell key={key}>{t(key)}</TableCell>)}</TableRow></TableHead><TableBody>
        {data.instances.map((row) => <TableRow key={row.id}><TableCell><Typography variant="body2">{row.hostname || row.id}</Typography><Typography variant="caption" color="text.secondary">{row.id}</Typography>{(row.error || row.last_error) && <Typography variant="caption" color="error" component="div">{row.error || row.last_error}</Typography>}</TableCell><TableCell>{row.node_role} / {row.billing_worker_mode === 'only' ? 'billing worker' : 'API'}</TableCell><TableCell>{row.worker_enabled ? t(row.worker_ready ? 'ready' : 'unavailable') : '—'}</TableCell><TableCell><Chip size="small" variant="outlined" color={row.health === 'up' ? 'success' : 'warning'} label={t(`state_${row.health}`)} /></TableCell><TableCell>{row.last_scrape ? new Date(row.last_scrape).toLocaleString() : '—'}</TableCell></TableRow>)}
        {!data.instances.length && <TableRow><TableCell colSpan={5}>{t('no_instances')}</TableCell></TableRow>}
      </TableBody></Table></TableContainer></Paper>
    </>}
  </Stack>;
}

function Settings({ t }) {
  const [settings, setSettings] = useState(null);
  const [form, setForm] = useState({ username: '', password: '', interval: 15, timeout: 5, retention: 30 });
  const [error, setError] = useState('');
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => { const abort = new AbortController(); request('/settings', { signal: abort.signal }).then((data) => { setSettings(data); setForm((old) => ({ ...old, username: data.metrics.username, interval: data.collection.interval_seconds, timeout: data.collection.timeout_seconds, retention: data.storage.retention_days })); }).catch((e) => { if (!abort.signal.aborted) setError(e.message); }); return () => abort.abort(); }, []);
  async function save() {
    setBusy(true); setError(''); setSaved(false);
    try {
      const payload = { metrics: { username: form.username, ...(form.password ? { password: form.password } : {}) }, collection: { interval_seconds: Number(form.interval), timeout_seconds: Number(form.timeout) }, storage: { retention_days: Number(form.retention) } };
      const data = await request('/settings', { method: 'put', data: payload }); setSettings(data); setForm((old) => ({ ...old, password: '' })); setSaved(true);
    } catch (e) { setError(e.response?.data?.message || e.message); } finally { setBusy(false); }
  }
  return <Paper variant="outlined" sx={{ p: 2, maxWidth: 960 }}>
    <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>{t('settings_intro')}</Typography>
    <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: '1fr 1fr' }, gap: 2 }}>
      {['username', 'password', 'interval', 'timeout', 'retention'].map((key) => <Box key={key}>
        <Typography variant="caption" component="div" color="text.secondary" sx={{ mb: 0.5 }}>{t(key)}</Typography>
        <TextField fullWidth size="small" type={(key === 'password') ? 'password' : ['interval', 'timeout', 'retention'].includes(key) ? 'number' : 'text'} value={form[key]} placeholder={(key === 'password') ? t('keep_secret') : t(key)} inputProps={{ 'aria-label': t(key) }} onChange={(e) => setForm((old) => ({ ...old, [key]: e.target.value }))} autoComplete="off" />
      </Box>)}
    </Box>
    {settings && <Box sx={{ mt: 2, overflowWrap: 'anywhere' }}><Typography variant="caption" color="text.secondary">{t('config_path')}</Typography><Typography variant="body2" sx={{ fontFamily: 'monospace', mb: 1 }}>{settings.config_path}</Typography><Typography variant="caption" color="text.secondary">{t('data_path')}</Typography><Typography variant="body2" sx={{ fontFamily: 'monospace' }}>{settings.resolved_data_path}</Typography><Typography variant="caption" color="text.secondary">{t('host_discovery')}</Typography></Box>}
    <Box sx={{ minHeight: 50, mt: 1 }}>{error && <Alert severity="error">{error}</Alert>}{saved && <Alert severity="success">{t('saved')}</Alert>}</Box>
    <Button variant="outlined" disabled={!settings || busy} onClick={save}>{t('save')}</Button>
  </Paper>;
}
