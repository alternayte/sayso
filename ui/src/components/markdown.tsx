import Markdown, { defaultUrlTransform } from "react-markdown";

/**
 * SafeMarkdown renders agent text (SI-16). Raw HTML is dropped, and a link
 * keeps its href only for http, https and mailto; other links render as text.
 */
export function SafeMarkdown({ text }: { text: string }) {
  return (
    <div className="prose-sayso">
      <Markdown
        skipHtml
        urlTransform={(url) => {
          const safe = defaultUrlTransform(url);
          return /^(https?:|mailto:|\/|#)/i.test(safe) ? safe : "";
        }}
        components={{
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
