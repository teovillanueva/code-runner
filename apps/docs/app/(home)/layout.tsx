import { HomeLayout } from 'fumadocs-ui/layouts/home';
import { baseOptions, navLinks } from '@/lib/layout.shared';

export default function Layout({ children }: LayoutProps<'/'>) {
  return (
    <HomeLayout {...baseOptions()} links={navLinks}>
      {children}
    </HomeLayout>
  );
}
