/** @type {import('@docusaurus/types').Config} */
const config = {
  title: 'Vigara Documentation',
  tagline: 'Guides and technical reference for Vigara',
  favicon: 'img/vigara-icon.png',
  url: 'https://deranjer.github.io',
  baseUrl: '/vigara/docs/',
  organizationName: 'deranjer',
  projectName: 'vigara',
  trailingSlash: true,
  onBrokenLinks: 'throw',
  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
  },
  presets: [['classic', {
    docs: {path: '../docs', routeBasePath: '/', sidebarPath: './sidebars.js'},
    blog: false,
    theme: {customCss: './src/css/custom.css'},
  }]],
  themeConfig: {
    navbar: {
      title: 'Vigara', logo: {alt: 'Vigara', src: 'img/vigara-icon.png'},
      items: [
        {type: 'docSidebar', sidebarId: 'docs', position: 'left', label: 'Documentation'},
        {href: 'https://github.com/deranjer/vigara', label: 'GitHub', position: 'right'},
      ],
    },
    footer: {
      style: 'dark',
      links: [{title: 'Project', items: [{label: 'GitHub repository', href: 'https://github.com/deranjer/vigara'}]}],
      copyright: `Copyright © ${new Date().getFullYear()} Vigara contributors.`,
    },
    prism: {additionalLanguages: ['bash', 'go', 'json', 'yaml']},
  },
};

export default config;
