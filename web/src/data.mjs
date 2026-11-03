export function requestParams(filters, now = Date.now()) {
  const end = filters.live ? Math.floor(now / 1000) : Math.floor(new Date(filters.end).getTime() / 1000);
  const start = filters.live ? end - Number(filters.range) : Math.floor(new Date(filters.start).getTime() / 1000);
  const duration = end - start;
  if (!Number.isFinite(start) || !Number.isFinite(end) || start <= 0 || duration <= 0 || duration > 30 * 86400) throw new Error('invalid_range');
  return {
    scope: filters.scope,
    start_timestamp: start,
    end_timestamp: end,
    grain_seconds: Math.max(15, Math.ceil(duration / 1499 / 15) * 15),
    ...Object.fromEntries(['model_name', 'channel_id', 'instance_id', 'api_mode', 'stream', 'stage'].filter((key) => filters[key]).map((key) => [key, filters[key]]))
  };
}

export function paths(points, stats, width = 600, height = 155) {
  const left = 42, top = 12, right = width - 12, bottom = height - 25;
  let max = 0;
  for (const p of points) for (const stat of stats) if (p.values[stat] != null) max = Math.max(max, p.values[stat]);
  const ceiling = max > 0 ? max * 1.1 : 1;
  const span = Math.max(1, (points.at(-1)?.timestamp || 1) - (points[0]?.timestamp || 0));
  return {
    ceiling, left, top, right, bottom,
    lines: stats.map((stat) => {
      let drawing = false, d = '';
      for (const p of points) {
        const value = p.values[stat];
        if (value == null || !Number.isFinite(value)) { drawing = false; continue; }
        const x = left + (p.timestamp - points[0].timestamp) / span * (right - left);
        const y = bottom - value / ceiling * (bottom - top);
        d += `${drawing ? 'L' : 'M'}${x.toFixed(2)},${y.toFixed(2)}${drawing ? '' : ' l0,0'} `;
        drawing = true;
      }
      return { stat, d };
    })
  };
}

export function formatValue(value, unit, language) {
  language = String(language || 'en').replaceAll('_', '-');
  if (value == null || !Number.isFinite(value)) return '—';
  if (unit === 'bytes') {
    const n = Math.max(0, Math.min(4, Math.floor(Math.log(Math.max(1, value)) / Math.log(1024))));
    return `${new Intl.NumberFormat(language, { maximumFractionDigits: 1 }).format(value / 1024 ** n)} ${['B', 'KiB', 'MiB', 'GiB', 'TiB'][n]}`;
  }
  return new Intl.NumberFormat(language, { maximumFractionDigits: value > 1000 ? 0 : 2 }).format(value);
}
