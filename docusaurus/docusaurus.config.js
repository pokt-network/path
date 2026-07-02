// @ts-check
// `@type` JSDoc annotations allow editor autocompletion and type checking
// (when paired with `@ts-check`).
// There are various equivalent ways to declare your Docusaurus config.
// See: https://docusaurus.io/docs/api/docusaurus-config

import { themes as prismThemes } from "prism-react-renderer";

/** @type {import('@docusaurus/types').Config} */
const config = {
  title: "Path",
  tagline: "All paths lead to Grove",
  favicon: "img/grove-leaf.jpeg",

  markdown: {
    mermaid: true,
  },
  themes: [
    "@docusaurus/theme-mermaid",
    "docusaurus-theme-openapi-docs",
    [
      require.resolve("@easyops-cn/docusaurus-search-local"),
      /** @type {import('@easyops-cn/docusaurus-search-local').PluginOptions} **/
      {
        docsRouteBasePath: "/",
        hashed: false,
        indexBlog: false,
        highlightSearchTermsOnTargetPage: true,
        explicitSearchResultPath: true,
      },
    ],
  ],

  // Set the production url of your site here.
  // Deployed via GitHub Pages to https://pokt-network.github.io/path/,
  // so baseUrl must be the "/path/" project subpath — otherwise assets
  // resolve to the domain root and 404.
  url: "https://pokt-network.github.io",
  baseUrl: "/path/",

  onBrokenLinks: "throw",

  i18n: {
    defaultLocale: "en",
    locales: ["en"],
  },

  presets: [
    [
      "classic",
      /** @type {import('@docusaurus/preset-classic').Options} */
      ({
        docs: {
          routeBasePath: "/",
          sidebarPath: "./sidebars.js",
          sidebarCollapsible: false,
          docItemComponent: "@theme/ApiItem",
          editUrl:
            "https://github.com/pokt-network/path/edit/main/docusaurus",
        },
        theme: {
          customCss: "./src/css/custom.css",
        },
      }),
    ],
  ],

  plugins: [
    [
      "docusaurus-plugin-openapi-docs",
      {
        id: "api",
        docsPluginId: "classic",
        config: {
          path: {
            specPath: "../api/path_openapi.yaml",
            outputDir: "docs/learn/api",
            sidebarOptions: {
              groupPathsBy: "tag",
            },
            downloadUrl: "../api/path_openapi.yaml",
            // Required to allow requests to production Grove Portal from the browser.
            proxy: "https://corsproxy.io/",
            hideSendButton: false,
          },
        },
      },
    ],
  ],

  themeConfig:
    /** @type {import('@docusaurus/preset-classic').ThemeConfig} */
    ({
      docs: {
        sidebar: {
          hideable: true,
          autoCollapseCategories: true,
        },
      },
      style: "dark",
      navbar: {
        title: "Path",
        logo: {
          alt: "Path logo",
          src: "img/grove-leaf.jpeg",
        },
        items: [
          {
            type: "docSidebar",
            position: "left",
            sidebarId: "developSidebar",
            label: "💻 Develop",
          },
          {
            type: "docSidebar",
            position: "left",
            sidebarId: "operateSidebar",
            label: "⚙️ Operate",
          },
          {
            type: "docSidebar",
            position: "left",
            sidebarId: "learnSidebar",
            label: "🧑‍🎓️ Learn",
          },
        ],
      },
      footer: {
        style: "dark",
        links: [
          {
            title: "Documentation",
            items: [
              {
                label: "Path",
                to: "/",
              },
              {
                label: "Path",
                href: "https://docs.grove.city/",
              },
            ],
          },
          {
            title: "Community",
            items: [
              {
                label: "Discord - Grove",
                href: "https://discord.gg/build-with-grove",
              },
              {
                label: "Twitter",
                href: "https://twitter.com/buildwithgrove",
              },
            ],
          },
          {
            title: "More",
            items: [
              {
                label: "GitHub",
                href: "https://github.com/buildwithgrove/path",
              },
            ],
          },
        ],
        copyright: `Grove Inc.`,
      },
      prism: {
        theme: prismThemes.github,
        darkTheme: prismThemes.dracula,
        additionalLanguages: [
          "gherkin",
          "protobuf",
          "json",
          "makefile",
          "diff",
          "lua",
          "bash",
        ],
      },
    }),
};

export default config;
