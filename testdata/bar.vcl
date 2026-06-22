/* routing
name: bar_service
hostnames:
  - bar.com
*/
vcl 4.1;
backend default { .host = "5.6.7.8"; }
