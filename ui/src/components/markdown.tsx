import Markdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

/**
 * SafeMarkdown renders agent text (SI-16) as GitHub-flavoured Markdown, so
 * the summary tables render as on GitHub. Raw HTML is dropped, and a link
 * keeps its href only for http, https and mailto; other links render as text.
 */
export function SafeMarkdown({ text }: { text: string }) {
  return (
    <div className="prose-sayso">
      <Markdown
        skipHtml
        remarkPlugins={[remarkGfm]}
        urlTransform={(url) => {
          const safe = defaultUrlTransform(url);
          return /^(https?:|mailto:|\/|#)/i.test(safe) ? safe : "";
        }}
        components={{
          table: ({ children }) => <Table>{children}</Table>,
          thead: ({ children }) => <TableHeader>{children}</TableHeader>,
          tbody: ({ children }) => <TableBody>{children}</TableBody>,
          tr: ({ children }) => <TableRow>{children}</TableRow>,
          th: ({ children }) => <TableHead>{children}</TableHead>,
          td: ({ children }) => <TableCell>{children}</TableCell>,
          a: ({ href, children }) =>
            href ? (
              <a href={href} className="link" rel="noopener noreferrer nofollow" target="_blank">
                {children}
              </a>
            ) : (
              <span>{children}</span>
            ),
        }}
      >
        {text}
      </Markdown>
    </div>
  );
}
