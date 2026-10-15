#!/usr/bin/env python3
"""
Relatorio de volume dos personagens (.snd) do Ikemen GO.

Usa a MESMA medicao do engine (src/volume_norm.go): RMS (dBFS) de todas as amostras
audiveis (acima de ~-50 dBFS) do .snd do personagem. O engine ja normaliza sozinho em
tempo de execucao (Config.ini: Sound.NormalizeChars / Sound.NormalizeTarget); este script
serve para CONFERIR o resultado e, opcionalmente, gravar o ganho no .def do personagem.

Uso:
  python3 tools/char_volume_report.py chars/            # relatorio
  python3 tools/char_volume_report.py chars/ --target -20 --csv volumes.csv
  python3 tools/char_volume_report.py chars/ --apply    # grava volumescale no .def (cria .bak)

ATENCAO: se usar --apply, desligue Sound.NormalizeChars no config para nao compensar duas vezes.
Requer: numpy, scipy.
"""
import argparse, io, math, os, re, struct, sys
import numpy as np
from scipy.io import wavfile

SILENCE = 0.003      # ~ -50 dBFS, igual ao engine
MIN_FRAMES = 4410    # menos de ~0,1 s audivel: ignora o personagem
MIN_GAIN, MAX_GAIN = 0.35, 2.0


def read_snd(path):
    """Le um SND v1 (ElecbyteSnd) e devolve lista de (grupo, numero, bytes_wav)."""
    with open(path, 'rb') as f:
        data = f.read()
    if data[:11] != b'ElecbyteSnd':
        raise ValueError('nao e um arquivo SND')
    count, offset = struct.unpack_from('<II', data, 16)
    out = []
    for _ in range(count):
        if offset == 0 or offset + 16 > len(data):
            break
        nxt, length, group, number = struct.unpack_from('<IIii', data, offset)
        wav = data[offset + 16: offset + 16 + length]
        out.append((group, number, wav))
        offset = nxt
    return out


def measure_wav(wav):
    """Devolve (soma_quadrados_ativos, frames_ativos, pico) em escala -1..1."""
    try:
        _, x = wavfile.read(io.BytesIO(wav))
    except Exception:
        return 0.0, 0, 0.0
    x = np.asarray(x)
    if x.dtype == np.uint8:
        x = (x.astype(np.float64) - 128) / 128
    elif x.dtype == np.int16:
        x = x.astype(np.float64) / 32768
    elif x.dtype == np.int32:
        x = x.astype(np.float64) / 2147483648
    else:
        x = x.astype(np.float64)
    if x.ndim == 2:
        mono = x.mean(axis=1)
        peak = float(np.abs(x).max()) if x.size else 0.0
    else:
        mono = x
        peak = float(np.abs(x).max()) if x.size else 0.0
    active = mono[np.abs(mono) > SILENCE]
    return float((active ** 2).sum()), int(active.size), peak


def analyze(snd_path):
    sq, frames, peak = 0.0, 0, 0.0
    for _, _, wav in read_snd(snd_path):
        s, n, p = measure_wav(wav)
        sq += s; frames += n; peak = max(peak, p)
    if frames < MIN_FRAMES or sq <= 0:
        return None
    return 20 * math.log10(math.sqrt(sq / frames)), peak


def gain_for(db, peak, target):
    g = 10 ** ((target - db) / 20)
    g = max(MIN_GAIN, min(MAX_GAIN, g))
    if g > 1 and peak > 0:           # nunca estourar ao aumentar
        g = max(1.0, min(g, 1 / peak))
    return g


def find_chars(root):
    for dirpath, _, files in os.walk(root):
        defs = [f for f in files if f.lower().endswith('.def')]
        for d in defs:
            p = os.path.join(dirpath, d)
            try:
                text = open(p, encoding='utf-8', errors='ignore').read()
            except OSError:
                continue
            m = re.search(r'^\s*sound\s*=\s*([^\s;]+)', text, re.I | re.M)
            if m:
                snd = os.path.join(dirpath, m.group(1).replace('\\', '/'))
                if os.path.isfile(snd):
                    yield p, snd


def apply_volumescale(def_path, scale_pct):
    text = open(def_path, encoding='utf-8', errors='ignore').read()
    with open(def_path + '.bak', 'w', encoding='utf-8') as f:
        f.write(text)
    if re.search(r'^\s*volumescale\s*=', text, re.I | re.M):
        text = re.sub(r'^(\s*volumescale\s*=\s*)[-\d.]+', lambda m: f'{m.group(1)}{scale_pct}', text, flags=re.I | re.M)
    else:
        text = re.sub(r'^(\s*\[data\][^\n]*\n)', lambda m: f'{m.group(1)}volumescale = {scale_pct}\n', text, count=1, flags=re.I | re.M)
    open(def_path, 'w', encoding='utf-8').write(text)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('root', help='pasta chars/')
    ap.add_argument('--target', type=float, default=-20.0, help='alvo RMS em dBFS (padrao -20)')
    ap.add_argument('--csv', help='salva o relatorio em CSV')
    ap.add_argument('--apply', action='store_true', help='grava volumescale no .def (cria .bak)')
    a = ap.parse_args()

    rows = []
    for def_path, snd in find_chars(a.root):
        try:
            r = analyze(snd)
        except Exception as e:
            print(f'[erro] {snd}: {e}', file=sys.stderr)
            continue
        if r is None:
            rows.append((def_path, None, None, 1.0))
            continue
        db, peak = r
        rows.append((def_path, db, peak, gain_for(db, peak, a.target)))

    rows.sort(key=lambda r: (r[1] is None, -(r[1] or 0)))
    print(f'{"personagem":60s} {"RMS dBFS":>9s} {"pico":>6s} {"ganho":>7s} {"volumescale":>11s}')
    for p, db, peak, g in rows:
        name = os.path.relpath(p, a.root)
        if db is None:
            print(f'{name:60.60s} {"(sem audio)":>9s}')
            continue
        print(f'{name:60.60s} {db:9.1f} {peak:6.2f} {g:7.2f} {round(100 * g):11d}')
        if a.apply:
            apply_volumescale(p, round(100 * g))
    valid = [r[1] for r in rows if r[1] is not None]
    if valid:
        print(f'\n{len(valid)} personagens | mais alto {max(valid):.1f} dB | mais baixo {min(valid):.1f} dB | '
              f'diferenca {max(valid) - min(valid):.1f} dB | alvo {a.target:.1f} dB')
    if a.csv:
        with open(a.csv, 'w', encoding='utf-8') as f:
            f.write('def,rms_db,peak,gain,volumescale\n')
            for p, db, peak, g in rows:
                f.write(f'{p},{"" if db is None else round(db, 2)},{"" if peak is None else round(peak, 3)},{round(g, 3)},{round(100 * g)}\n')


if __name__ == '__main__':
    main()
