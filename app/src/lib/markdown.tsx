import type { ReactNode } from 'react';

type Block =
  | { kind: 'p'; text: string }
  | { kind: 'h'; text: string }
  | { kind: 'ul'; items: string[] }
  | { kind: 'code'; text: string };

/** Splits the supported subset: paragraphs, headings, dash lists, fenced code. Everything else is plain text. */
export function parseBlocks(source: string): Block[] {
  const blocks: Block[] = [];
  const lines = source.replace(/\r\n?/g, '\n').split('\n');
  let para: string[] = [];
  let list: string[] | null = null;
  const flush = () => {
    if (para.length > 0) blocks.push({ kind: 'p', text: para.join(' ') });
    if (list) blocks.push({ kind: 'ul', items: list });
    para = [];
    list = null;
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? '';
    if (line.trimStart().startsWith('```')) {
      flush();
      const code: string[] = [];
      for (i++; i < lines.length && !(lines[i] ?? '').trimStart().startsWith('```'); i++) code.push(lines[i] ?? '');
      blocks.push({ kind: 'code', text: code.join('\n') });
      continue;
    }
    const heading = /^#{1,6}\s+(.+)$/.exec(line);
    const item = /^\s*[-*]\s+(.+)$/.exec(line);
    if (line.trim() === '') flush();
    else if (heading?.[1]) {
      flush();
      blocks.push({ kind: 'h', text: heading[1] });
    } else if (item?.[1]) {
      if (para.length > 0) flush();
      (list ??= []).push(item[1]);
    } else {
      if (list) flush();
      para.push(line.trim());
    }
  }
  flush();
  return blocks;
}

/** `code` and **bold** only; React escapes the rest, so no markup from the source ever reaches the DOM. */
export function Inline({ text }: { text: string }): ReactNode {
  return text.split(/(`[^`]+`|\*\*[^*]+\*\*)/).map((part, i) => {
    if (part.length > 2 && part.startsWith('`') && part.endsWith('`')) {
      return (
        <code key={i} className="rounded-md bg-surface2 px-1.5 py-px font-mono text-[13px]">
          {part.slice(1, -1)}
        </code>
      );
    }
    if (part.length > 4 && part.startsWith('**') && part.endsWith('**')) return <strong key={i}>{part.slice(2, -2)}</strong>;
    return part;
  });
}

/** Markdown subset with raw HTML and links disabled, for ticket briefs and hints. */
export function Markdown({ source }: { source: string }) {
  return (
    <div className="flex flex-col gap-3 text-[15px] leading-[1.55] text-fg2">
      {parseBlocks(source).map((b, i) => {
        if (b.kind === 'code') {
          return (
            <pre key={i} tabIndex={0} className="m-0 overflow-x-auto rounded-[10px] bg-surface2 p-3 font-mono text-[13px] text-fg">
              {b.text}
            </pre>
          );
        }
        if (b.kind === 'h') {
          return (
            <h2 key={i} className="m-0 text-base font-extrabold text-fg">
              <Inline text={b.text} />
            </h2>
          );
        }
        if (b.kind === 'ul') {
          return (
            <ul key={i} className="m-0 list-disc pl-5">
              {b.items.map((t, j) => (
                <li key={j}>
                  <Inline text={t} />
                </li>
              ))}
            </ul>
          );
        }
        return (
          <p key={i} className="m-0">
            <Inline text={b.text} />
          </p>
        );
      })}
    </div>
  );
}
